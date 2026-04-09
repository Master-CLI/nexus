package config

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"runtime"

	"gopkg.in/yaml.v3"
)

// defaultShell returns a platform-appropriate default shell.
func defaultShell() string {
	if runtime.GOOS == "windows" {
		return "powershell.exe"
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/sh"
}

type Config struct {
	MCPBasePort int             `yaml:"mcp_base_port"`
	GatewayURL  string          `yaml:"gateway_url"`
	WorkDir     string          `yaml:"work_dir"`
	MaxSessions int             `yaml:"max_sessions"`
	PTY              PTYConfig         `yaml:"pty"`
	Capture          CaptureConfig     `yaml:"capture"`
	MCP              MCPConfig         `yaml:"mcp"`
	ExternalMCPServers map[string]string `yaml:"external_mcp_servers"` // name → URL
	ProfileDir  string          `yaml:"profile_dir"`
	Theme       ThemeConfig     `yaml:"theme"`
}

type ThemeConfig struct {
	Background string `yaml:"background" json:"background"`
	Foreground string `yaml:"foreground" json:"foreground"`
	Cursor     string `yaml:"cursor" json:"cursor"`
	FontFamily string `yaml:"font_family" json:"font_family"`
	FontSize   int    `yaml:"font_size" json:"font_size"`
}

type PTYConfig struct {
	Shell string `yaml:"shell"`
	Cols  int    `yaml:"cols"`
	Rows  int    `yaml:"rows"`
}

type CaptureConfig struct {
	IdleTimeoutSec      int      `yaml:"idle_timeout_sec"`
	AgentIdleTimeoutSec int      `yaml:"agent_idle_timeout_sec"`
	ExtraPromptPatterns []string `yaml:"extra_prompt_patterns"` // user-defined regex for prompt detection
}

type MCPConfig struct {
	AuthEnabled bool   `yaml:"auth_enabled"`
	BearerToken string `yaml:"bearer_token"` // if empty and auth_enabled, auto-generated at startup
}

func Load(path string) (*Config, error) {
	cfg := &Config{
		MCPBasePort: 9400,
		GatewayURL:  "",
		WorkDir:     "",
		MaxSessions: 8,
		PTY: PTYConfig{
			Shell: defaultShell(),
			Cols:  120,
			Rows:  30,
		},
		Capture: CaptureConfig{
			IdleTimeoutSec:      5,
			AgentIdleTimeoutSec: 15,
		},
		MCP: MCPConfig{
			AuthEnabled: false,
		},
		ProfileDir: "nexus-profiles",
	}
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	// Auto-generate bearer token if auth is enabled but no token is set.
	if cfg.MCP.AuthEnabled && cfg.MCP.BearerToken == "" {
		cfg.MCP.BearerToken = generateToken()
	}

	return cfg, nil
}

// generateToken creates a random 32-char hex bearer token.
func generateToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
