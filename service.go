package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/anthropic/nexus/internal/config"
	"github.com/anthropic/nexus/internal/session"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// NexusService is the Wails-bound service exposing RPC methods to the frontend.
type NexusService struct {
	app      *application.App
	registry *session.Registry
	config   *config.Config
	counter  atomic.Int64
}

type AgentTerminalOptions struct {
	AgentType   string
	Name        string
	InitMessage string
	Model       string
	WorkDir     string
	MCPServers  []string
}

// Ping is a health check called from the frontend.
func (s *NexusService) Ping() string {
	return "ok"
}

// GetVersion returns build metadata.
func (s *NexusService) GetVersion() map[string]string {
	return map[string]string{
		"commit":     Commit,
		"build_time": BuildTime,
	}
}

// CreateTerminal spawns a new PTY session and returns its ID.
// Optional name parameter; defaults to "term-N".
func (s *NexusService) CreateTerminal(name string) (string, error) {
	n := s.counter.Add(1)
	id := fmt.Sprintf("term-%d", n)
	if name == "" {
		name = id
	}
	_, err := s.registry.Create(id, name, s.config.PTY.Shell, s.config.PTY.Cols, s.config.PTY.Rows, s.config.WorkDir)
	if err != nil {
		return "", err
	}
	return id, nil
}

// CreateAgentTerminal spawns an agent terminal (claude/codex/gemini/shell)
// with MCP auto-discovery configured. Returns session ID.
// initMessage is passed as a CLI argument for reliable delivery (no PTY race).
func (s *NexusService) CreateAgentTerminal(agentType, name, initMessage, model string) (string, error) {
	return s.CreateAgentTerminalWithOptions(AgentTerminalOptions{
		AgentType:   agentType,
		Name:        name,
		InitMessage: initMessage,
		Model:       model,
	})
}

func (s *NexusService) CreateAgentTerminalWithOptions(opts AgentTerminalOptions) (string, error) {
	var id string
	for {
		n := s.counter.Add(1)
		id = fmt.Sprintf("agent-%d", n)
		if s.registry.Get(id) == nil {
			break
		}
	}
	n := s.counter.Load()
	if opts.Name == "" {
		opts.Name = fmt.Sprintf("%s-%d", opts.AgentType, n)
	}

	workDir := opts.WorkDir
	if workDir == "" {
		workDir = s.config.WorkDir
	}

	cfg := session.LaunchConfig{
		AgentType:   session.AgentType(opts.AgentType),
		Name:        opts.Name,
		Model:       opts.Model,
		MCPPort:     s.config.MCPBasePort,
		GatewayURL:  s.config.GatewayURL,
		MCPServers:  opts.MCPServers,
		WorkDir:     workDir,
		InitMessage: opts.InitMessage,
		Cols:        s.config.PTY.Cols,
		Rows:        s.config.PTY.Rows,
		Shell:       s.config.PTY.Shell,
	}

	_, err := s.registry.CreateAgent(id, cfg)
	if err != nil {
		return "", err
	}
	return id, nil
}

// RestoreTerminal re-creates a PTY session with a specific ID (used on restart
// when flexlayout restores tabs from localStorage but PTY processes are gone).
func (s *NexusService) RestoreTerminal(sessionID, agentType, name string) error {
	// Skip if already exists.
	if s.registry.Get(sessionID) != nil {
		return nil
	}
	if agentType == "" || agentType == "shell" {
		if name == "" {
			name = sessionID
		}
		_, err := s.registry.Create(sessionID, name, s.config.PTY.Shell, s.config.PTY.Cols, s.config.PTY.Rows, s.config.WorkDir)
		return err
	}
	if name == "" {
		name = sessionID
	}
	cfg := session.LaunchConfig{
		AgentType:  session.AgentType(agentType),
		Name:       name,
		MCPPort:    s.config.MCPBasePort,
		GatewayURL: s.config.GatewayURL,
		WorkDir:    s.config.WorkDir,
		Cols:       s.config.PTY.Cols,
		Rows:       s.config.PTY.Rows,
		Shell:      s.config.PTY.Shell,
	}
	_, err := s.registry.CreateAgent(sessionID, cfg)
	return err
}

// ListTerminals returns info about all active sessions.
func (s *NexusService) ListTerminals() []session.SessionInfo {
	return s.registry.ListDetails()
}

// WriteTerminal sends keystrokes to a PTY session.
// CPR filtering is handled in readLoop (session.go), not here.
func (s *NexusService) WriteTerminal(sessionID string, data string) error {
	sess := s.registry.Get(sessionID)
	if sess == nil {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	_, err := sess.PTY.Write([]byte(data))
	return err
}

// ResizeTerminal changes PTY dimensions.
func (s *NexusService) ResizeTerminal(sessionID string, cols, rows int) error {
	sess := s.registry.Get(sessionID)
	if sess == nil {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	return sess.PTY.Resize(cols, rows)
}

// DestroyTerminal kills a PTY session.
func (s *NexusService) DestroyTerminal(sessionID string) error {
	return s.registry.Destroy(sessionID)
}

// GetThemeConfig returns the terminal theme config for the frontend.
func (s *NexusService) GetThemeConfig() config.ThemeConfig {
	return s.config.Theme
}

// MCPServerInfo describes one MCP server connection for the status bar.
type MCPServerInfo struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	Connected bool   `json:"connected"`
}

// GetMCPServers returns all MCP servers (built-in + project .mcp.json),
// with live connectivity check (similar to /mcp in Claude Code).
func (s *NexusService) GetMCPServers() []MCPServerInfo {
	var servers []MCPServerInfo

	// Add configured external MCP servers.
	for name, url := range s.config.ExternalMCPServers {
		servers = append(servers, MCPServerInfo{Name: name, URL: url})
	}

	// Read project .mcp.json for additional servers
	mcpPath := filepath.Join(s.config.WorkDir, ".mcp.json")
	if data, err := os.ReadFile(mcpPath); err == nil {
		var mcpCfg struct {
			Servers map[string]struct {
				URL string `json:"url"`
			} `json:"mcpServers"`
		}
		if json.Unmarshal(data, &mcpCfg) == nil {
			for name, srv := range mcpCfg.Servers {
				servers = append(servers, MCPServerInfo{Name: name, URL: srv.URL})
			}
		}
	}

	// Quick TCP check for each
	for i := range servers {
		conn, err := net.DialTimeout("tcp", extractHost(servers[i].URL), 1*time.Second)
		if err == nil {
			servers[i].Connected = true
			conn.Close()
		}
	}
	return servers
}

// extractHost pulls "localhost:9400" from "http://localhost:9400/sse".
func extractHost(url string) string {
	// Strip scheme
	s := url
	if idx := len("http://"); len(s) > idx && s[:idx] == "http://" {
		s = s[idx:]
	} else if idx := len("https://"); len(s) > idx && s[:idx] == "https://" {
		s = s[idx:]
	}
	// Strip path
	for i, c := range s {
		if c == '/' {
			return s[:i]
		}
	}
	return s
}
