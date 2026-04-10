package session

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
)

// Scheduler manages cron/on_idle/on_event triggers for agent profiles.
type Scheduler struct {
	mu       sync.Mutex
	profiles *ProfileStore
	registry *Registry
	running  map[string]string // profile name → session ID (prevents duplicate runs)
	launchers map[string]context.CancelFunc // profile name → cancel func for scheduled goroutines
	managedMode bool // true = skip internal cron, let external controller (Dashboard) manage scheduling

	createSession func(agentType, name, initMessage, model string) (string, error)
}

// NewScheduler creates a profile scheduler.
func NewScheduler(profiles *ProfileStore, registry *Registry, managedMode bool) *Scheduler {
	return &Scheduler{
		profiles:    profiles,
		registry:    registry,
		running:     make(map[string]string),
		launchers:   make(map[string]context.CancelFunc),
		managedMode: managedMode,
	}
}

// SetCreateSession sets the callback for creating sessions.
func (s *Scheduler) SetCreateSession(fn func(agentType, name, initMessage, model string) (string, error)) {
	s.createSession = fn
}

// Start begins monitoring all profiles with schedule configurations.
// In managed mode, cron scheduling is disabled — only manual RunProfile works.
func (s *Scheduler) Start(ctx context.Context) {
	profiles := s.profiles.List()
	if s.managedMode {
		log.Printf("[scheduler] managed mode — cron disabled, %d profiles available for manual run", len(profiles))
		return
	}
	for _, p := range profiles {
		if p.Schedule.Cron != "" {
			s.startCron(ctx, p)
		}
	}
	log.Printf("[scheduler] started, monitoring %d profiles", len(profiles))
}

// RunProfile manually launches an agent from a profile.
func (s *Scheduler) RunProfile(name string) (string, error) {
	s.mu.Lock()
	if sid, ok := s.running[name]; ok {
		s.mu.Unlock()
		return sid, nil // already running
	}
	s.mu.Unlock()

	p, err := s.profiles.Get(name)
	if err != nil {
		return "", err
	}

	return s.launchProfile(p)
}

// destroySessionsByName removes all sessions matching a display name.
// Handles orphaned sessions restored from localStorage after restart.
func (s *Scheduler) destroySessionsByName(name string) {
	for _, info := range s.registry.ListDetails() {
		if info.Name == name {
			log.Printf("[scheduler] destroying session %q (%s)", info.ID, info.Name)
			_ = s.registry.Destroy(info.ID)
		}
	}
}

// StopProfile stops a running profile session and cleans up orphans.
func (s *Scheduler) StopProfile(name string) error {
	s.mu.Lock()
	delete(s.running, name)
	s.mu.Unlock()

	s.destroySessionsByName(name)
	return nil
}

// IsRunning returns whether a profile is currently active.
func (s *Scheduler) IsRunning(name string) (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sid, ok := s.running[name]
	return ok, sid
}

func (s *Scheduler) launchProfile(p *AgentProfile) (string, error) {
	if s.createSession == nil {
		return "", nil
	}

	s.destroySessionsByName(p.Name)

	sessionID, err := s.createSession(p.Provider, p.Name, p.InitMessage, p.Model)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	s.running[p.Name] = sessionID
	s.mu.Unlock()

	// Auto-terminate after max_runtime_sec if set.
	if p.Limits.MaxRuntimeSec > 0 {
		go func() {
			time.Sleep(time.Duration(p.Limits.MaxRuntimeSec) * time.Second)
			s.mu.Lock()
			if sid, ok := s.running[p.Name]; ok && sid == sessionID {
				delete(s.running, p.Name)
				s.mu.Unlock()
				_ = s.registry.Destroy(sessionID)
				log.Printf("[scheduler] profile %q reached max_runtime (%ds), terminated", p.Name, p.Limits.MaxRuntimeSec)
			} else {
				s.mu.Unlock()
			}
		}()
	}

	// Loop mode: monitor session and relaunch when it ends.
	if p.Schedule.Loop {
		go s.loopMonitor(p, sessionID)
	}

	log.Printf("[scheduler] launched profile %q → session %s (loop=%v)", p.Name, sessionID, p.Schedule.Loop)
	return sessionID, nil
}

// loopMonitor watches a session and relaunches the profile when it exits.
// Stops when the profile is removed from s.running (i.e. user called StopProfile).
func (s *Scheduler) loopMonitor(p *AgentProfile, sessionID string) {
	const pollInterval = 3 * time.Second
	const restartDelay = 5 * time.Second

	for {
		time.Sleep(pollInterval)

		// Check if profile was stopped by user.
		s.mu.Lock()
		currentSID, ok := s.running[p.Name]
		s.mu.Unlock()
		if !ok || currentSID != sessionID {
			log.Printf("[scheduler] loop stopped for %q (removed from running)", p.Name)
			return
		}

		// Check if session is still alive.
		sess := s.registry.Get(sessionID)
		if sess != nil && sess.IsAlive() {
			continue // still running
		}

		// Session ended — clean up and relaunch.
		log.Printf("[scheduler] loop: session %q ended, restarting in %v", p.Name, restartDelay)
		s.mu.Lock()
		delete(s.running, p.Name)
		s.mu.Unlock()

		if sess != nil {
			_ = s.registry.Destroy(sessionID)
		}

		time.Sleep(restartDelay)

		// Re-check user didn't stop during delay.
		s.mu.Lock()
		if _, stillStopped := s.running[p.Name]; stillStopped {
			s.mu.Unlock()
			return // someone else started it
		}
		s.mu.Unlock()

		newSID, err := s.launchProfile(p)
		if err != nil {
			log.Printf("[scheduler] loop: relaunch failed for %q: %v", p.Name, err)
			return
		}
		sessionID = newSID
	}
}

// startCron starts a goroutine that runs a profile on a simple cron schedule.
// Supports basic patterns: "*/N" for every N hours, or hour-based scheduling.
func (s *Scheduler) startCron(ctx context.Context, p *AgentProfile) {
	interval := parseCronInterval(p.Schedule.Cron)
	if interval <= 0 {
		log.Printf("[scheduler] unsupported cron expression for %q: %s", p.Name, p.Schedule.Cron)
		return
	}

	childCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.launchers[p.Name] = cancel
	s.mu.Unlock()

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		log.Printf("[scheduler] cron started for %q: every %v", p.Name, interval)

		for {
			select {
			case <-childCtx.Done():
				return
			case <-ticker.C:
				if running, _ := s.IsRunning(p.Name); running {
					continue // skip if already running
				}
				if _, err := s.launchProfile(p); err != nil {
					log.Printf("[scheduler] cron launch failed for %q: %v", p.Name, err)
				}
			}
		}
	}()
}

// parseCronInterval converts simple cron patterns to a duration.
// Supports: "0 */6 * * *" (every 6h), "0 0 * * *" (daily), etc.
func parseCronInterval(expr string) time.Duration {
	parts := strings.Fields(expr)
	if len(parts) < 5 {
		return 0
	}

	// Check hour field for */N pattern.
	hourPart := parts[1]
	if strings.HasPrefix(hourPart, "*/") {
		var n int
		if _, err := time.ParseDuration("1h"); err == nil {
			fmt := hourPart[2:]
			for _, c := range fmt {
				if c >= '0' && c <= '9' {
					n = n*10 + int(c-'0')
				}
			}
		}
		if n > 0 {
			return time.Duration(n) * time.Hour
		}
	}

	// "0 0 * * *" = daily
	if hourPart == "0" && parts[2] == "*" {
		return 24 * time.Hour
	}

	// Default: every 6 hours.
	return 6 * time.Hour
}
