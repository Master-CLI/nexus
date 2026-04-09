package session

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// SessionInfo is the public view of a session (for MCP tools / frontend).
type SessionInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	AgentType string `json:"agent_type"`
	Busy      bool   `json:"busy"`
	Alive     bool   `json:"alive"`
}

// Session represents a single agent terminal session.
type Session struct {
	ID        string
	Name      string
	AgentType AgentType
	PTY       *PTY
	LaunchDir string // temp dir to clean up on destroy (may be empty)
	done      chan struct{}

	// alive is set to 0 when readLoop exits (process exited/crashed).
	alive atomic.Int32

	// Output capture: when non-nil, readLoop copies output here too.
	captureMu   sync.Mutex
	captureCh   chan string  // receives output chunks while capture is active
	captureOpen atomic.Int32 // 1 if captureCh is open, 0 if closed/nil

	// Optional session log file.
	logMu   sync.Mutex
	logFile *os.File
}

// StartCapture begins capturing output from this session.
// Returns a channel that receives output chunks.
// Call StopCapture when done.
func (s *Session) StartCapture() <-chan string {
	s.captureMu.Lock()
	defer s.captureMu.Unlock()
	ch := make(chan string, 256)
	s.captureCh = ch
	s.captureOpen.Store(1)
	return ch
}

// StopCapture stops capturing output.
func (s *Session) StopCapture() {
	s.captureMu.Lock()
	defer s.captureMu.Unlock()
	if s.captureCh != nil {
		s.captureOpen.Store(0) // mark closed BEFORE closing the channel
		close(s.captureCh)
		s.captureCh = nil
	}
}

// sendToCapture sends data to the capture channel if active.
// Safe against closed channel panic via atomic flag check.
func (s *Session) sendToCapture(data string) {
	if s.captureOpen.Load() == 0 {
		return
	}
	s.captureMu.Lock()
	ch := s.captureCh
	s.captureMu.Unlock()
	if ch != nil {
		select {
		case ch <- data:
		default:
			// Drop if buffer full — better than blocking the readLoop.
		}
	}
}

// IsAlive returns true if the session's process is still running.
func (s *Session) IsAlive() bool {
	return s.alive.Load() == 1
}

// ReadPeerResult is the response from ReadPeer — terminal state snapshot.
type ReadPeerResult struct {
	Target    string   `json:"target"`
	Status    string   `json:"status"` // "idle" | "busy" | "error" | "dead"
	AtPrompt  bool     `json:"at_prompt"`
	LastLines []string `json:"last_lines"`
}

// Registry manages all active terminal sessions.
type Registry struct {
	mu          sync.RWMutex
	sessions    map[string]*Session
	maxSessions int
	emitFn      func(topic string, data any)
	logDir      string // optional directory for session logs

	// Read Guard: tracks caller→target→timestamp of last read_peer call.
	guardMu sync.Mutex
	guards  map[string]map[string]time.Time // guards[callerID][targetID] = time
}

const readGuardTTL = 30 * time.Second

// NewRegistry creates a session registry. emitFn is called to emit events
// to the frontend (typically app.Event.Emit).
func NewRegistry(emitFn func(string, any), maxSessions int) *Registry {
	if maxSessions <= 0 {
		maxSessions = 8
	}

	// Set up log directory.
	logDir := ""
	home, err := os.UserHomeDir()
	if err == nil {
		logDir = filepath.Join(home, ".nexus", "logs")
		os.MkdirAll(logDir, 0755)
	}

	return &Registry{
		sessions:    make(map[string]*Session),
		maxSessions: maxSessions,
		emitFn:      emitFn,
		logDir:      logDir,
		guards:      make(map[string]map[string]time.Time),
	}
}

// Create spawns a new shell PTY session.
func (r *Registry) Create(id, name, shell string, cols, rows int, workDir string) (*Session, error) {
	return r.createPTY(id, name, AgentShell, shell, cols, rows, workDir, "")
}

// CreateAgent spawns an agent terminal (claude/codex/gemini) with MCP auto-discovery.
func (r *Registry) CreateAgent(id string, cfg LaunchConfig) (*Session, error) {
	command, workDir, launchDir, err := BuildCommand(cfg)
	if err != nil {
		return nil, fmt.Errorf("build command: %w", err)
	}
	return r.createPTY(id, cfg.Name, cfg.AgentType, command, cfg.Cols, cfg.Rows, workDir, launchDir)
}

func (r *Registry) createPTY(id, name string, agentType AgentType, command string, cols, rows int, workDir, launchDir string) (*Session, error) {
	r.mu.Lock()
	if _, exists := r.sessions[id]; exists {
		r.mu.Unlock()
		return nil, fmt.Errorf("session %q already exists", id)
	}
	if len(r.sessions) >= r.maxSessions {
		r.mu.Unlock()
		return nil, fmt.Errorf("max sessions reached (%d), cannot create more", r.maxSessions)
	}
	r.mu.Unlock()

	pty, err := NewPTY(command, cols, rows, workDir)
	if err != nil {
		return nil, fmt.Errorf("conpty start: %w", err)
	}

	s := &Session{
		ID:        id,
		Name:      name,
		AgentType: agentType,
		PTY:       pty,
		LaunchDir: launchDir,
		done:      make(chan struct{}),
	}
	s.alive.Store(1)

	// Open session log file if log directory is configured.
	if r.logDir != "" {
		logPath := filepath.Join(r.logDir, id+".log")
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err == nil {
			s.logFile = f
			ts := time.Now().Format("2006-01-02 15:04:05")
			fmt.Fprintf(f, "=== Session %s (%s) started at %s ===\n", id, name, ts)
		}
	}

	r.mu.Lock()
	r.sessions[id] = s
	r.mu.Unlock()

	go r.readLoop(s)
	if agentType == AgentClaude {
		go r.autoAcceptClaudeTrustPrompt(s)
	}
	log.Printf("[session] created %q (cmd=%s, %dx%d, workDir=%s)", id, command, cols, rows, workDir)
	return s, nil
}

// Get returns a session by ID, or nil if not found.
func (r *Registry) Get(id string) *Session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sessions[id]
}

// Destroy closes a session and removes it from the registry.
func (r *Registry) Destroy(id string) error {
	r.mu.Lock()
	s, ok := r.sessions[id]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("session %q not found", id)
	}
	delete(r.sessions, id)
	r.mu.Unlock()

	s.StopCapture()
	err := s.PTY.Close()

	// Wait for readLoop with timeout to avoid blocking forever.
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		log.Printf("[session] readLoop timeout for %q, forcing cleanup", id)
	}

	// Close session log file.
	s.logMu.Lock()
	if s.logFile != nil {
		ts := time.Now().Format("2006-01-02 15:04:05")
		fmt.Fprintf(s.logFile, "\n=== Session ended at %s ===\n", ts)
		s.logFile.Close()
		s.logFile = nil
	}
	s.logMu.Unlock()

	// Clean up temp launch directory.
	if s.LaunchDir != "" {
		if removeErr := os.RemoveAll(s.LaunchDir); removeErr != nil {
			log.Printf("[session] failed to clean launch dir %s: %v", s.LaunchDir, removeErr)
		} else {
			log.Printf("[session] cleaned launch dir: %s", s.LaunchDir)
		}
	}

	log.Printf("[session] destroyed %q", id)

	// Notify frontend to remove the tab from the dock layout.
	if r.emitFn != nil {
		r.emitFn("nexus:session:destroyed", map[string]any{
			"session_id": id,
		})
	}

	return err
}

// List returns IDs of all active sessions.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.sessions))
	for id := range r.sessions {
		ids = append(ids, id)
	}
	return ids
}

// ListDetails returns detailed info for all active sessions.
func (r *Registry) ListDetails() []SessionInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]SessionInfo, 0, len(r.sessions))
	for _, s := range r.sessions {
		s.captureMu.Lock()
		busy := s.captureCh != nil
		s.captureMu.Unlock()
		if !busy {
			rawTail := normalizeTerminalText(r.readLogTail(s.ID, 80))
			busy = providerLooksBusy(s.AgentType, rawTail)
		}
		out = append(out, SessionInfo{
			ID:        s.ID,
			Name:      s.Name,
			AgentType: string(s.AgentType),
			Busy:      busy,
			Alive:     s.IsAlive(),
		})
	}
	return out
}

// WriteToSession sends data to a session's PTY stdin.
func (r *Registry) WriteToSession(id string, data []byte) error {
	s := r.Get(id)
	if s == nil {
		return fmt.Errorf("session %q not found", id)
	}
	_, err := s.PTY.Write(data)
	return err
}

// Shutdown closes all sessions.
func (r *Registry) Shutdown() {
	r.mu.RLock()
	ids := make([]string, 0, len(r.sessions))
	for id := range r.sessions {
		ids = append(ids, id)
	}
	r.mu.RUnlock()
	for _, id := range ids {
		r.Destroy(id)
	}
}

// ReadPeer captures a snapshot of a target session's terminal state.
// Also records a read guard for the caller→target pair.
func (r *Registry) ReadPeer(callerID, targetID string, lines int) (*ReadPeerResult, error) {
	s := r.Get(targetID)
	if s == nil {
		return nil, fmt.Errorf("session %q not found", targetID)
	}

	if lines <= 0 {
		lines = 50
	}

	result := &ReadPeerResult{Target: targetID}

	// Check alive status first.
	if !s.IsAlive() {
		result.Status = "dead"
		result.LastLines = []string{"(process exited)"}
		r.recordGuard(callerID, targetID)
		return result, nil
	}

	// Capture a brief snapshot: start capture, collect for a short window, stop.
	ch := s.StartCapture()
	var chunks []string
	timeout := time.After(500 * time.Millisecond)
drain:
	for {
		select {
		case chunk, ok := <-ch:
			if !ok {
				break drain
			}
			chunks = append(chunks, chunk)
		case <-timeout:
			break drain
		}
	}
	s.StopCapture()

	// Read the session log tail for last N lines (more reliable than capture snapshot).
	lastOutput := strings.Join(chunks, "")
	if lastOutput == "" {
		// No new output during snapshot — read from log file.
		lastOutput = r.readLogTail(targetID, lines)
	}

	snapshotClean := filterAgentNoise(s.AgentType, stripTUIArtifacts(lastOutput))
	logClean := filterAgentNoise(s.AgentType, stripTUIArtifacts(r.readLogTail(targetID, max(lines*4, 80))))
	clean := bestPeerView(s.AgentType, snapshotClean, logClean)
	allLines := peerViewLines(s.AgentType, clean)
	if len(allLines) > lines {
		allLines = allLines[len(allLines)-lines:]
	}
	// Remove empty trailing lines.
	for len(allLines) > 0 && allLines[len(allLines)-1] == "" {
		allLines = allLines[:len(allLines)-1]
	}
	result.LastLines = allLines

	// Determine status from output.
	rawStatusView := normalizeTerminalText(lastOutput + "\n" + r.readLogTail(targetID, max(lines*4, 80)))
	if providerAtPrompt(s.AgentType, rawStatusView) {
		result.Status = "idle"
		result.AtPrompt = true
	} else {
		// Check for error indicators in the last few lines only (not startup banners).
		// Use line-start patterns to avoid false positives from incidental "error" in prose.
		result.Status = "busy"
		tail := clean
		if len(tail) > 500 {
			tail = tail[len(tail)-500:]
		}
		lower := strings.ToLower(tail)
		for _, pattern := range []string{
			"error:", "error!", "panic:", "traceback ",
			"fatal:", "exception:", "failed:",
		} {
			if strings.Contains(lower, pattern) {
				result.Status = "error"
				break
			}
		}
	}

	// Record guard.
	r.recordGuard(callerID, targetID)
	return result, nil
}

// recordGuard records that callerID has read targetID's state.
func (r *Registry) recordGuard(callerID, targetID string) {
	r.guardMu.Lock()
	defer r.guardMu.Unlock()
	if r.guards[callerID] == nil {
		r.guards[callerID] = make(map[string]time.Time)
	}
	r.guards[callerID][targetID] = time.Now()
}

// CheckGuard returns nil if callerID has a valid (non-expired) read guard for targetID.
func (r *Registry) CheckGuard(callerID, targetID string) error {
	r.guardMu.Lock()
	defer r.guardMu.Unlock()
	targets, ok := r.guards[callerID]
	if !ok {
		return fmt.Errorf("read guard required: call read_peer(%q) before send_to_peer", targetID)
	}
	t, ok := targets[targetID]
	if !ok {
		return fmt.Errorf("read guard required: call read_peer(%q) before send_to_peer", targetID)
	}
	if time.Since(t) > readGuardTTL {
		delete(targets, targetID)
		return fmt.Errorf("read guard expired (>%v): call read_peer(%q) again", readGuardTTL, targetID)
	}
	return nil
}

// ClearGuard removes the read guard for a caller→target pair (called after send_to_peer).
func (r *Registry) ClearGuard(callerID, targetID string) {
	r.guardMu.Lock()
	defer r.guardMu.Unlock()
	if targets, ok := r.guards[callerID]; ok {
		delete(targets, targetID)
	}
}

// readLogTail reads the last N lines from a session's log file.
func (r *Registry) readLogTail(sessionID string, lines int) string {
	if r.logDir == "" {
		return ""
	}
	logPath := filepath.Join(r.logDir, sessionID+".log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		return ""
	}
	all := strings.Split(string(data), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n")
}

// readLoop reads PTY output and emits events to the frontend.
// If a capture is active, output is also sent to the capture channel.
func (r *Registry) readLoop(s *Session) {
	defer func() {
		s.alive.Store(0)
		close(s.done)
	}()

	buf := make([]byte, 32*1024)
	for {
		n, err := s.PTY.Reader().Read(buf)
		if n > 0 {
			data := buf[:n]

			// Filter CPR responses at the source (before frontend/capture).
			if isCPR(data) {
				continue
			}

			str := string(data)

			// Write to session log.
			s.logMu.Lock()
			if s.logFile != nil {
				s.logFile.WriteString(str)
			}
			s.logMu.Unlock()

			// Emit to frontend.
			r.emitFn("nexus:terminal:output", map[string]any{
				"session_id": s.ID,
				"data":       str,
			})
			// Send to capture if active.
			s.sendToCapture(str)
		}
		if err != nil {
			if err != io.EOF {
				log.Printf("[session] read error for %q: %v", s.ID, err)
			}
			r.emitFn("nexus:terminal:output", map[string]any{
				"session_id": s.ID,
				"type":       "exit",
			})
			return
		}
	}
}

func (r *Registry) autoAcceptClaudeTrustPrompt(s *Session) {
	deadline := time.Now().Add(20 * time.Second)
	sent := false

	for time.Now().Before(deadline) {
		if !s.IsAlive() {
			return
		}

		tail := normalizeTerminalText(r.readLogTail(s.ID, 120))
		compact := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(tail, " ", ""), ",", ""))
		if !strings.Contains(compact, "itrustthisfolder") {
			time.Sleep(400 * time.Millisecond)
			continue
		}
		if sent {
			return
		}

		if _, err := s.PTY.Write([]byte("1")); err != nil {
			log.Printf("[session] failed to auto-accept Claude trust prompt for %q: %v", s.ID, err)
			return
		}
		time.Sleep(150 * time.Millisecond)
		if _, err := s.PTY.Write(EnterKey(s.AgentType)); err != nil {
			log.Printf("[session] failed to confirm Claude trust prompt for %q: %v", s.ID, err)
			return
		}
		sent = true
		log.Printf("[session] auto-accepted Claude trust prompt for %q", s.ID)
		time.Sleep(2 * time.Second)
	}
}

// isCPR returns true if data is a Cursor Position Report response (\x1b[row;colR).
func isCPR(data []byte) bool {
	n := len(data)
	if n < 4 || data[0] != 0x1b || data[1] != '[' || data[n-1] != 'R' {
		return false
	}
	for i := 2; i < n-1; i++ {
		if data[i] != ';' && (data[i] < '0' || data[i] > '9') {
			return false
		}
	}
	return true
}

func peerViewLines(agentType AgentType, text string) []string {
	if lines := providerTranscriptLines(agentType, text); len(lines) > 0 {
		return trimTrailingEmptyLines(lines)
	}
	return trimTrailingEmptyLines(strings.Split(text, "\n"))
}

func bestPeerView(agentType AgentType, snapshotClean, logClean string) string {
	switch agentType {
	case AgentClaude, AgentCodex, AgentGemini:
		snapshotReply := providerReply(agentType, snapshotClean)
		logReply := providerReply(agentType, logClean)
		switch {
		case logReply != "":
			return logClean
		case snapshotReply != "":
			return snapshotClean
		case len(logClean) > len(snapshotClean):
			return logClean
		default:
			return snapshotClean
		}
	default:
		if len(snapshotClean) >= len(logClean) {
			return snapshotClean
		}
		return logClean
	}
}

func max(a, b int) int {
	if a >= b {
		return a
	}
	return b
}

func trimTrailingEmptyLines(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
