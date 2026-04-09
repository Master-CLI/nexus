package session

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// AgentProfile defines a declarative agent role template loaded from YAML.
type AgentProfile struct {
	Name        string        `yaml:"name" json:"name"`
	Description string        `yaml:"description" json:"description"`
	Provider    string        `yaml:"provider" json:"provider"` // claude | codex | gemini
	Model       string        `yaml:"model" json:"model,omitempty"` // e.g. "sonnet", "claude-sonnet-4-6", "gpt-5.4"
	InitMessage string        `yaml:"init_message" json:"init_message"`
	Schedule    ProfileSchedule `yaml:"schedule" json:"schedule,omitempty"`
	Limits      ProfileLimits   `yaml:"limits" json:"limits,omitempty"`
}

// ProfileSchedule defines when to auto-trigger this profile.
type ProfileSchedule struct {
	Cron    string `yaml:"cron" json:"cron,omitempty"`        // cron expression e.g. "0 */6 * * *"
	Loop    bool   `yaml:"loop" json:"loop,omitempty"`        // restart immediately after session ends
	OnIdle  bool   `yaml:"on_idle" json:"on_idle,omitempty"`  // run when system is idle
	OnEvent string `yaml:"on_event" json:"on_event,omitempty"` // event trigger expression
}

// ProfileLimits defines runtime constraints for the profile.
type ProfileLimits struct {
	MaxRuntimeSec  int      `yaml:"max_runtime_sec" json:"max_runtime_sec,omitempty"`
	AllowedTools   []string `yaml:"allowed_tools" json:"allowed_tools,omitempty"`
	ForbiddenTools []string `yaml:"forbidden_tools" json:"forbidden_tools,omitempty"`
	Namespace      string   `yaml:"namespace" json:"namespace,omitempty"` // restrict RAG writes to this namespace
	MaxSessions    int      `yaml:"max_sessions" json:"max_sessions,omitempty"` // how many concurrent instances
}

// ProfileStore manages loading and listing agent profiles.
type ProfileStore struct {
	dir      string
	profiles map[string]*AgentProfile
}

// NewProfileStore creates a profile store and loads profiles from the given directory.
func NewProfileStore(dir string) *ProfileStore {
	ps := &ProfileStore{
		dir:      dir,
		profiles: make(map[string]*AgentProfile),
	}
	ps.Load()
	return ps
}

// Load scans the profile directory for .yaml files and loads them.
func (ps *ProfileStore) Load() {
	if ps.dir == "" {
		return
	}
	entries, err := os.ReadDir(ps.dir)
	if err != nil {
		log.Printf("[profile] cannot read profile dir %s: %v", ps.dir, err)
		return
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		path := filepath.Join(ps.dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			log.Printf("[profile] read error %s: %v", path, err)
			continue
		}
		var p AgentProfile
		if err := yaml.Unmarshal(data, &p); err != nil {
			log.Printf("[profile] parse error %s: %v", path, err)
			continue
		}
		if p.Name == "" {
			p.Name = strings.TrimSuffix(e.Name(), ext)
		}
		if p.Provider == "" {
			p.Provider = "claude"
		}
		ps.profiles[p.Name] = &p
		log.Printf("[profile] loaded: %s (provider=%s)", p.Name, p.Provider)
	}

	log.Printf("[profile] %d profiles loaded from %s", len(ps.profiles), ps.dir)
}

// List returns all loaded profiles.
func (ps *ProfileStore) List() []*AgentProfile {
	result := make([]*AgentProfile, 0, len(ps.profiles))
	for _, p := range ps.profiles {
		result = append(result, p)
	}
	return result
}

// Get returns a profile by name.
func (ps *ProfileStore) Get(name string) (*AgentProfile, error) {
	p, ok := ps.profiles[name]
	if !ok {
		return nil, fmt.Errorf("profile %q not found", name)
	}
	return p, nil
}
