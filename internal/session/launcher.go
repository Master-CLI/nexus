package session

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
)

// ExternalMCPConfig represents one external MCP server from config.
type ExternalMCPConfig struct {
	URL string `yaml:"url"`
}

// AgentType identifies the CLI agent to launch.
type AgentType string

const (
	AgentShell  AgentType = "shell"
	AgentClaude AgentType = "claude"
	AgentCodex  AgentType = "codex"
	AgentGemini AgentType = "gemini"
)

// LaunchConfig holds parameters for creating an agent terminal.
type LaunchConfig struct {
	AgentType   AgentType
	Name        string
	Model       string   // optional model override (e.g. "sonnet", "claude-sonnet-4-6")
	MCPPort     int      // Nexus MCP port (e.g., 9400)
	GatewayURL  string   // Gateway SSE URL (e.g., http://localhost:9200/sse)
	MCPServers  []string // Optional per-session MCP server names (nexus is always included)
	WorkDir     string   // Project root for file access
	InitMessage string   // Initial prompt passed as CLI arg (avoids PTY write race)
	Cols        int
	Rows        int
	Shell       string // default shell for AgentShell
}

// findBinary locates a CLI binary on PATH.
func findBinary(name string) string {
	path, err := exec.LookPath(name)
	if err != nil {
		return name
	}
	return path
}

func preferCmdCompatibleBinary(path string) string {
	if runtime.GOOS != "windows" {
		return path
	}

	ext := strings.ToLower(filepath.Ext(path))
	base := path
	if ext != "" {
		base = strings.TrimSuffix(path, ext)
	}

	candidates := []string{}
	switch ext {
	case ".ps1":
		candidates = append(candidates, base+".cmd", base+".exe")
	case "":
		candidates = append(candidates, path+".cmd", path+".exe")
	}

	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return path
}

// prepareLaunchDir creates a temp directory for provider-specific support files.
// AuthToken is set at startup if MCP auth is enabled. Injected into explicit MCP configs.
var AuthToken string

func prepareLaunchDir(agentType AgentType, label string, mcpPort int, gatewayURL, workDir string) (string, error) {
	base := filepath.Join(os.TempDir(), "nexus-launch")
	if err := os.MkdirAll(base, 0755); err != nil {
		return "", fmt.Errorf("mkdir launch base: %w", err)
	}

	prefix := sanitizeLaunchLabel(fmt.Sprintf("%s-%s", agentType, label))
	if prefix == "" {
		prefix = string(agentType)
	}
	dir, err := os.MkdirTemp(base, prefix+"-")
	if err != nil {
		return "", fmt.Errorf("mktemp launch dir: %w", err)
	}
	log.Printf("[launcher] prepared launch dir: %s", dir)
	return dir, nil
}

func writeSessionMCPConfig(dir string, mcpPort int, gatewayURL string, selected []string) (string, error) {
	servers := make(map[string]map[string]any)
	for name, url := range sessionMCPServerURLs(mcpPort, gatewayURL, selected) {
		servers[name] = map[string]any{
			"type": "http",
			"url":  url,
		}
	}
	data, err := json.MarshalIndent(map[string]any{"mcpServers": servers}, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, ".mcp.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", fmt.Errorf("write .mcp.json: %w", err)
	}
	return path, nil
}

func writeClaudeBootstrapFile(dir, workDir string) (string, error) {
	content := []string{
		"# Nexus Session",
		"",
		"Start from the current workspace directory.",
		"Treat this session as blank at startup.",
		"Do not assume any prior identity, forum role, or previous conversation unless the current user explicitly asks for it.",
		"For simple greetings or health checks, reply in one short sentence.",
		"Do not connect extra MCP servers unless the user explicitly asks for them.",
	}
	if workDir != "" {
		content = append(content, "", fmt.Sprintf("Workspace root: `%s`.", filepath.Clean(workDir)))
	}
	path := filepath.Join(dir, "CLAUDE_SESSION.md")
	if err := os.WriteFile(path, []byte(strings.Join(content, "\n")+"\n"), 0644); err != nil {
		return "", err
	}
	return path, nil
}

func sanitizeLaunchLabel(label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return ""
	}

	var b strings.Builder
	lastDash := false
	for _, r := range label {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
			lastDash = false
		case !lastDash:
			b.WriteByte('-')
			lastDash = true
		}
	}

	return strings.Trim(b.String(), "-")
}

func effectiveWorkDir(workDir string) string {
	if strings.TrimSpace(workDir) != "" {
		return workDir
	}
	if cwd, err := os.Getwd(); err == nil && strings.TrimSpace(cwd) != "" {
		return cwd
	}
	return ""
}

// ExternalMCPServers maps logical names to MCP endpoint URLs.
// Populated from config at startup. Nexus always adds itself automatically.
var ExternalMCPServers = map[string]string{}

func defaultSessionMCPServers() []string {
	return []string{"nexus"}
}

func normalizeSessionMCPServers(requested []string) []string {
	if len(requested) == 0 {
		return defaultSessionMCPServers()
	}

	seen := map[string]bool{}
	out := make([]string, 0, len(requested)+1)
	add := func(name string) {
		name = strings.TrimSpace(strings.ToLower(name))
		if name == "" || seen[name] {
			return
		}
		// Allow "nexus" (always) and any externally registered server.
		if name != "nexus" {
			if _, ok := ExternalMCPServers[name]; !ok {
				return
			}
		}
		seen[name] = true
		out = append(out, name)
	}

	add("nexus")
	for _, name := range requested {
		add(name)
	}
	return out
}

func sessionMCPServerURLs(mcpPort int, gatewayURL string, selected []string) map[string]string {
	selected = normalizeSessionMCPServers(selected)
	urls := make(map[string]string, len(selected))
	for _, name := range selected {
		if name == "nexus" {
			nexusURL := fmt.Sprintf("http://localhost:%d/mcp", mcpPort)
			if AuthToken != "" {
				nexusURL += "?token=" + AuthToken
			}
			urls[name] = nexusURL
			continue
		}
		if u, ok := ExternalMCPServers[name]; ok {
			urls[name] = u
		}
	}
	return urls
}

func codexMCPAddCommands(urls map[string]string) string {
	// Collect all server names for deterministic ordering.
	names := make([]string, 0, len(urls))
	for name := range urls {
		names = append(names, name)
	}
	parts := make([]string, 0, len(names)*2)

	// Remove known servers first to clear stale global registrations.
	for _, name := range names {
		parts = append(parts, fmt.Sprintf(`codex mcp remove %s 2>nul`, name))
	}
	// Then add only the servers needed for this session.
	for _, name := range names {
		parts = append(parts, fmt.Sprintf(`codex mcp add %s --url %s 2>nul`, name, urls[name]))
	}
	return strings.Join(parts, " & ")
}

func geminiMCPAddCommands(urls map[string]string) string {
	parts := make([]string, 0, len(urls))
	for name, url := range urls {
		parts = append(parts, fmt.Sprintf(`gemini mcp add %s %s --scope user --transport http 2>nul`, name, url))
	}
	return strings.Join(parts, " & ")
}

func geminiAllowedMCPFlags(selected []string) string {
	parts := make([]string, 0, len(selected))
	for _, name := range normalizeSessionMCPServers(selected) {
		parts = append(parts, fmt.Sprintf(` --allowed-mcp-server-names %s`, name))
	}
	return strings.Join(parts, "")
}

// BuildCommand returns the command string, working directory, and launch dir
// (for temp cleanup) for ConPTY.
func BuildCommand(cfg LaunchConfig) (command string, workDir string, launchDir string, err error) {
	switch cfg.AgentType {
	case AgentShell:
		shell := cfg.Shell
		if shell == "" {
			if runtime.GOOS == "windows" {
				shell = "powershell.exe"
			} else if sh := os.Getenv("SHELL"); sh != "" {
				shell = sh
			} else {
				shell = "/bin/sh"
			}
		}
		return shell, effectiveWorkDir(cfg.WorkDir), "", nil

	case AgentClaude:
		lDir, err := prepareLaunchDir(AgentClaude, cfg.Name, cfg.MCPPort, cfg.GatewayURL, cfg.WorkDir)
		if err != nil {
			return "", "", "", err
		}
		bin := findBinary("claude")
		mcpConfig, err := writeSessionMCPConfig(lDir, cfg.MCPPort, cfg.GatewayURL, cfg.MCPServers)
		if err != nil {
			return "", "", "", err
		}
		bootstrapFile, err := writeClaudeBootstrapFile(lDir, cfg.WorkDir)
		if err != nil {
			return "", "", "", err
		}
		wd := effectiveWorkDir(cfg.WorkDir)
		if wd == "" {
			wd = lDir
		}
		cmd := fmt.Sprintf(`%s --dangerously-skip-permissions --strict-mcp-config --mcp-config %s --append-system-prompt-file %s`, bin, mcpConfig, bootstrapFile)

		// Optional model override (e.g. "sonnet", "claude-sonnet-4-6").
		if cfg.Model != "" {
			cmd += fmt.Sprintf(` --model %s`, cfg.Model)
		}

		// If init_message is set, write to a temp file and pass as positional arg.
		// This is far more reliable than writing to PTY after startup.
		if cfg.InitMessage != "" {
			promptFile := filepath.Join(lDir, "init_prompt.txt")
			if err := os.WriteFile(promptFile, []byte(cfg.InitMessage), 0644); err != nil {
				log.Printf("[launcher] failed to write init_prompt.txt: %v", err)
			} else {
				// Claude Code reads the prompt from a file via shell redirection workaround:
				// Use --resume-conversation-id with a prompt file isn't directly supported,
				// but we can use the positional [prompt] argument with proper quoting.
				// For multi-line messages, use the --system-prompt approach instead:
				// the system prompt sets the agent's role, and we send a short trigger as prompt.
				cmd += fmt.Sprintf(` --append-system-prompt-file %s`, promptFile)
				cmd += ` "Begin your work cycle now. Use the MCP tools available to you."`
			}
		}

		return cmd, wd, lDir, nil

	case AgentCodex:
		lDir, err := prepareLaunchDir(AgentCodex, cfg.Name, cfg.MCPPort, cfg.GatewayURL, cfg.WorkDir)
		if err != nil {
			return "", "", "", err
		}
		bin := preferCmdCompatibleBinary(findBinary("codex"))
		wd := effectiveWorkDir(cfg.WorkDir)
		urls := sessionMCPServerURLs(cfg.MCPPort, cfg.GatewayURL, cfg.MCPServers)
		// Use `codex mcp add` commands to register servers before launch,
		// avoiding -c flag quoting issues that cause MCP boot failures on Windows.
		cmd := `cmd /c "`
		if addCmds := codexMCPAddCommands(urls); addCmds != "" {
			cmd += addCmds + ` & `
		}
		cmd += fmt.Sprintf(`"%s" --cd "%s" --dangerously-bypass-approvals-and-sandbox --disable apps`,
			bin, wd,
		)

		// Pass init_message as a prompt file piped to Codex.
		// Codex reads [PROMPT] as a positional arg; for long messages we write
		// to a file and use a wrapper .bat that passes its content.
		if cfg.InitMessage != "" {
			promptFile := filepath.Join(lDir, "init_prompt.txt")
			if err := os.WriteFile(promptFile, []byte(cfg.InitMessage), 0644); err != nil {
				log.Printf("[launcher] failed to write codex init_prompt.txt: %v", err)
			} else {
				// Write a wrapper .bat that reads the prompt from file and passes
				// it as stdin to codex (exec mode isn't suitable — we need interactive).
				// Instead, write a short trigger as positional arg and inject the full
				// prompt via --system-prompt config override.
				cmd += fmt.Sprintf(` -c "instructions_file=\"%s\""`, filepath.ToSlash(promptFile))
				cmd += ` "Read the file referenced in instructions_file config, then follow those instructions immediately. Use the MCP tools available to you.""`
			}
		} else {
			cmd += `"`
		}
		return cmd, wd, lDir, nil

	case AgentGemini:
		lDir, err := prepareLaunchDir(AgentGemini, cfg.Name, cfg.MCPPort, cfg.GatewayURL, cfg.WorkDir)
		if err != nil {
			return "", "", "", err
		}
		bin := preferCmdCompatibleBinary(findBinary("gemini"))
		urls := sessionMCPServerURLs(cfg.MCPPort, cfg.GatewayURL, cfg.MCPServers)
		wd := effectiveWorkDir(cfg.WorkDir)
		cmd := `cmd /c "`
		if addCmds := geminiMCPAddCommands(urls); addCmds != "" {
			cmd += addCmds + ` & `
		}
		cmd += fmt.Sprintf(`"%s" --yolo%s`, bin, geminiAllowedMCPFlags(cfg.MCPServers))

		// Pass init_message via --prompt-interactive so Gemini processes it
		// at startup then continues interactively for the discussion loop.
		if cfg.InitMessage != "" {
			promptFile := filepath.Join(lDir, "init_prompt.txt")
			if err := os.WriteFile(promptFile, []byte(cfg.InitMessage), 0644); err != nil {
				log.Printf("[launcher] failed to write gemini init_prompt.txt: %v", err)
			} else {
				// Gemini's -i flag runs a prompt then stays interactive.
				// For long messages, pass a short trigger and inject full context
				// via the prompt file path reference.
				cmd += fmt.Sprintf(` -i "Read the file at %s and follow those instructions immediately. Use the MCP tools available to you."`, filepath.ToSlash(promptFile))
			}
		}
		cmd += `"`
		return cmd, wd, lDir, nil

	default:
		return "", "", "", fmt.Errorf("unknown agent type: %s", cfg.AgentType)
	}
}
