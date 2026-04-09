package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPreferCmdCompatibleBinaryUsesCmdShimOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-specific shim selection")
	}

	dir := t.TempDir()
	ps1 := filepath.Join(dir, "codex.ps1")
	cmd := filepath.Join(dir, "codex.cmd")
	if err := os.WriteFile(ps1, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cmd, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	if got := preferCmdCompatibleBinary(ps1); got != cmd {
		t.Fatalf("preferCmdCompatibleBinary(%q) = %q, want %q", ps1, got, cmd)
	}
}

func TestPreferCmdCompatibleBinaryLeavesExeUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude.exe")
	if got := preferCmdCompatibleBinary(path); got != path {
		t.Fatalf("preferCmdCompatibleBinary(%q) = %q, want unchanged", path, got)
	}
}

func TestPrepareLaunchDirCreatesUniqueTempDir(t *testing.T) {
	dir1, err := prepareLaunchDir(AgentGemini, "gemini-probe", 9400, "", t.TempDir())
	if err != nil {
		t.Fatalf("prepareLaunchDir(dir1): %v", err)
	}
	defer os.RemoveAll(dir1)

	dir2, err := prepareLaunchDir(AgentGemini, "gemini-probe", 9400, "", t.TempDir())
	if err != nil {
		t.Fatalf("prepareLaunchDir(dir2): %v", err)
	}
	defer os.RemoveAll(dir2)

	if dir1 == dir2 {
		t.Fatalf("prepareLaunchDir() reused directory %q", dir1)
	}
	if _, err := os.Stat(dir1); err != nil {
		t.Fatalf("launch dir missing: %v", err)
	}
}

func TestWriteSessionMCPConfigIncludesOnlyNexusByDefault(t *testing.T) {
	// No external servers configured, no gateway.
	old := ExternalMCPServers
	ExternalMCPServers = map[string]string{}
	defer func() { ExternalMCPServers = old }()

	dir := t.TempDir()
	path, err := writeSessionMCPConfig(dir, 9400, "", nil)
	if err != nil {
		t.Fatalf("writeSessionMCPConfig(): %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read .mcp.json: %v", err)
	}
	var cfg struct {
		Servers map[string]struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse .mcp.json: %v", err)
	}
	if len(cfg.Servers) != 1 {
		t.Fatalf("server count = %d, want 1 (only nexus)", len(cfg.Servers))
	}
	server, ok := cfg.Servers["nexus"]
	if !ok {
		t.Fatal(".mcp.json missing server 'nexus'")
	}
	if server.URL != "http://localhost:9400/mcp" {
		t.Fatalf("nexus url = %q, want http://localhost:9400/mcp", server.URL)
	}
}

func TestWriteSessionMCPConfigIncludesExternalServers(t *testing.T) {
	old := ExternalMCPServers
	ExternalMCPServers = map[string]string{
		"my-rag":     "http://localhost:9090/mcp",
		"my-browser": "http://localhost:9500/mcp",
	}
	defer func() { ExternalMCPServers = old }()

	dir := t.TempDir()
	path, err := writeSessionMCPConfig(dir, 9400, "", []string{"my-rag", "my-browser"})
	if err != nil {
		t.Fatalf("writeSessionMCPConfig(): %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read .mcp.json: %v", err)
	}
	var cfg struct {
		Servers map[string]struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse .mcp.json: %v", err)
	}
	for _, name := range []string{"nexus", "my-rag", "my-browser"} {
		if _, ok := cfg.Servers[name]; !ok {
			t.Fatalf(".mcp.json missing server %q", name)
		}
	}
}

func TestNormalizeSessionMCPServersRejectsUnregistered(t *testing.T) {
	old := ExternalMCPServers
	ExternalMCPServers = map[string]string{}
	defer func() { ExternalMCPServers = old }()

	got := normalizeSessionMCPServers([]string{"nexus", "unknown-server"})
	if len(got) != 1 || got[0] != "nexus" {
		t.Fatalf("normalizeSessionMCPServers() = %v, want [nexus]", got)
	}
}

func TestBuildCommandShellAutoDetectsShell(t *testing.T) {
	cmd, _, _, err := BuildCommand(LaunchConfig{
		AgentType: AgentShell,
		Name:      "test-shell",
	})
	if err != nil {
		t.Fatalf("BuildCommand(): %v", err)
	}
	if cmd == "" {
		t.Fatal("BuildCommand() returned empty command for shell")
	}
}

func TestBuildCommandClaudeIncludesBootstrapFile(t *testing.T) {
	workDir := t.TempDir()
	cmd, _, launchDir, err := BuildCommand(LaunchConfig{
		AgentType: AgentClaude,
		Name:      "test-claude",
		MCPPort:   9400,
		WorkDir:   workDir,
	})
	if err != nil {
		t.Fatalf("BuildCommand(): %v", err)
	}
	if launchDir != "" {
		defer os.RemoveAll(launchDir)
	}

	for _, fragment := range []string{
		"claude",
		"--dangerously-skip-permissions",
		"--strict-mcp-config",
		"--mcp-config",
		"CLAUDE_SESSION.md",
	} {
		if !strings.Contains(cmd, fragment) {
			t.Fatalf("command missing fragment %q:\n%s", fragment, cmd)
		}
	}
}
