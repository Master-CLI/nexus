package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/Master-CLI/nexus/internal/config"
	nexusmcp "github.com/Master-CLI/nexus/internal/mcp"
	"github.com/Master-CLI/nexus/internal/session"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:internal/embed
var assets embed.FS

var (
	Commit    = "dev"
	BuildTime = "unknown"
)

func resolveProfileDir(profileDir, cfgPath, workDir string) string {
	if profileDir == "" || filepath.IsAbs(profileDir) {
		return profileDir
	}
	if cfgPath != "" {
		cfgDir := filepath.Dir(cfgPath)
		if absCfgDir, err := filepath.Abs(cfgDir); err == nil {
			return filepath.Join(absCfgDir, profileDir)
		}
		return filepath.Join(cfgDir, profileDir)
	}
	if workDir != "" {
		return filepath.Join(workDir, profileDir)
	}
	return profileDir
}

func main() {
	headless := flag.Bool("headless", false, "MCP-only mode, no GUI")
	workdir := flag.String("workdir", "", "Override working directory (used by context-menu integration)")
	flag.Parse()

	// Load config.
	cfgPath := "nexus-config.yaml"
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		cfgPath = ""
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	// CLI -workdir overrides config.
	if *workdir != "" {
		cfg.WorkDir = *workdir
	}

	// Set auth token for launcher to inject into .mcp.json.
	if cfg.MCP.AuthEnabled && cfg.MCP.BearerToken != "" {
		session.AuthToken = cfg.MCP.BearerToken
	}

	// Wire external MCP servers from config.
	for name, url := range cfg.ExternalMCPServers {
		session.ExternalMCPServers[name] = url
	}

	svc := &NexusService{config: cfg}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Load agent profiles.
	profileDir := resolveProfileDir(cfg.ProfileDir, cfgPath, cfg.WorkDir)
	profileStore := session.NewProfileStore(profileDir)

	// Create MCP server (registry wired after app/registry creation).
	mcpServer := nexusmcp.NewServer(nil, cfg)
	mcpServer.SetProfileStore(profileStore)

	if *headless {
		// Headless mode: MCP-only, no GUI.
		log.Printf("[main] Nexus running in headless mode (MCP on :%d)", cfg.MCPBasePort)
		svc.registry = session.NewRegistry(func(topic string, data any) {}, cfg.MaxSessions)
		mcpServer.SetRegistry(svc.registry)
		mcpServer.SetCreateSession(func(req nexusmcp.CreateSessionRequest) (string, error) {
			if req.AgentType == "shell" {
				return svc.CreateTerminal(req.Name)
			}
			return svc.CreateAgentTerminalWithOptions(AgentTerminalOptions{
				AgentType:   req.AgentType,
				Name:        req.Name,
				InitMessage: req.InitMessage,
				Model:       req.Model,
				WorkDir:     req.WorkDir,
				MCPServers:  req.MCPServers,
			})
		})
		mcpServer.SetDestroySession(func(sessionID string) error {
			return svc.DestroyTerminal(sessionID)
		})

		// Wire scheduler. InitMessage flows through LaunchConfig → CLI args.
		scheduler := session.NewScheduler(profileStore, svc.registry, false)
		scheduler.SetCreateSession(func(agentType, name, initMessage, model string) (string, error) {
			id, err := svc.CreateAgentTerminal(agentType, name, initMessage, model)
			if err != nil {
				return "", err
			}
			return id, nil
		})
		mcpServer.SetScheduler(scheduler)
		scheduler.Start(ctx)

		go mcpServer.Listen(ctx, cfg.MCPBasePort)

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("[main] Nexus shutting down")
		svc.registry.Shutdown()
	} else {
		// Full GUI mode.
		frontendFS, err := fs.Sub(assets, "internal/embed")
		if err != nil {
			log.Fatalf("embedded assets: %v", err)
		}

		app := application.New(application.Options{
			Name:        "Nexus",
			Description: fmt.Sprintf("Terminal Hub (%s @ %s)", Commit, BuildTime),
			Assets: application.AssetOptions{
				Handler: application.AssetFileServerFS(frontendFS),
			},
			Services: []application.Service{
				application.NewService(svc),
			},
		})

		// Wire app reference for event emission.
		svc.app = app
		svc.registry = session.NewRegistry(func(topic string, data any) {
			app.Event.Emit(topic, data)
		}, cfg.MaxSessions)
		mcpServer.SetRegistry(svc.registry)
		mcpServer.SetDestroySession(func(sessionID string) error {
			return svc.DestroyTerminal(sessionID)
		})
		mcpServer.SetCreateSession(func(req nexusmcp.CreateSessionRequest) (string, error) {
			var id string
			var err error
			if req.AgentType == "shell" {
				id, err = svc.CreateTerminal(req.Name)
			} else {
				id, err = svc.CreateAgentTerminalWithOptions(AgentTerminalOptions{
					AgentType:   req.AgentType,
					Name:        req.Name,
					InitMessage: req.InitMessage,
					Model:       req.Model,
					WorkDir:     req.WorkDir,
					MCPServers:  req.MCPServers,
				})
			}
			if err != nil {
				return "", err
			}
			// Notify frontend to add a tab in the dock layout.
			app.Event.Emit("nexus:session:created", map[string]any{
				"session_id": id,
				"agent_type": req.AgentType,
				"name":       req.Name,
			})
			return id, nil
		})

		// Wire scheduler with GUI event emission.
		// InitMessage is passed through LaunchConfig → CLI args (no PTY write race).
		scheduler := session.NewScheduler(profileStore, svc.registry, false)
		scheduler.SetCreateSession(func(agentType, name, initMessage, model string) (string, error) {
			id, err := svc.CreateAgentTerminal(agentType, name, initMessage, model)
			if err != nil {
				return "", err
			}
			app.Event.Emit("nexus:session:created", map[string]any{
				"session_id": id,
				"agent_type": agentType,
				"name":       name,
			})
			return id, nil
		})
		mcpServer.SetScheduler(scheduler)
		scheduler.Start(ctx)

		go mcpServer.Listen(ctx, cfg.MCPBasePort)

		winTitle := fmt.Sprintf("Nexus — Terminal Hub [%s@%s]", Commit, BuildTime)
		if cfg.WorkDir != "" {
			winTitle = fmt.Sprintf("Nexus — %s [%s@%s]", cfg.WorkDir, Commit, BuildTime)
		}
		app.Window.NewWithOptions(application.WebviewWindowOptions{
			Title:  winTitle,
			Width:  1024,
			Height: 700,
			URL:    "/",
			Windows: application.WindowsWindow{
				Theme: application.Dark,
			},
		})

		if err := app.Run(); err != nil {
			log.Fatal(err)
		}
		svc.registry.Shutdown()
	}
}
