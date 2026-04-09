package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/anthropic/nexus/internal/session"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

const (
	defaultAskTimeout  = 30 * time.Second
	initMessageTimeout = 60 * time.Second // max wait for agent prompt before sending init_message
	maxBroadcastPeers  = 10               // max peers in a broadcast
	maxBroadcastOutput = 32000            // total output limit for broadcast responses
)

// Simple per-tool rate limiter: tracks last call time per tool name.
var (
	rateMu    sync.Mutex
	rateLimit = map[string]time.Duration{
		"broadcast": 10 * time.Second,
		// create_session has no rate limit — Forum legitimately creates multiple sessions in parallel.
	}
	lastCall = map[string]time.Time{}
)

func bootstrapInitMessage(agentType, initMessage string) string {
	if initMessage == "" || agentType == "shell" {
		return initMessage
	}

	lines := []string{
		"Treat this Nexus-started session as fresh and blank at startup.",
		"Do not proactively summarize the repository, inspect cross-module requests, or infer a task from the workspace path alone.",
		"Use files, MCP tools, and project context only when they are relevant to the current prompt.",
	}
	if agentType == "gemini" {
		lines = append(lines, "For simple greetings or health checks, ignore incidental repository context and reply in one short sentence.")
	}
	lines = append(lines, "", initMessage)
	return strings.Join(lines, "\n")
}

func appendContextPacket(initMessage string, packet map[string]any) string {
	if len(packet) == 0 {
		return initMessage
	}

	data, err := json.MarshalIndent(packet, "", "  ")
	if err != nil {
		return initMessage
	}

	lines := []string{
		"Session context packet (authoritative for this session):",
		string(data),
		"Use this packet as the primary session context when deciding role, tools, and expected output.",
	}
	if strings.TrimSpace(initMessage) != "" {
		lines = append(lines, "", initMessage)
	}
	return strings.Join(lines, "\n")
}

func stringSliceArg(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

func objectArg(v any) map[string]any {
	raw, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return raw
}

func checkRateLimit(tool string) error {
	limit, ok := rateLimit[tool]
	if !ok {
		return nil
	}
	rateMu.Lock()
	defer rateMu.Unlock()
	if last, exists := lastCall[tool]; exists {
		if time.Since(last) < limit {
			return fmt.Errorf("%s rate limited: wait %v", tool, limit-time.Since(last))
		}
	}
	lastCall[tool] = time.Now()
	return nil
}

func (s *Server) registerTools(srv *mcpserver.MCPServer) {
	srv.AddTool(mcp.Tool{
		Name:        "create_session",
		Description: "Create a new terminal tab in the Nexus UI. Returns the new session ID. Use init_message to give the agent an initial instruction (e.g., role, domain expertise). The terminal appears as a new tab in the dock layout.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"type": map[string]any{
					"type":        "string",
					"enum":        []string{"shell", "claude", "codex", "gemini"},
					"description": "Type of terminal to create.",
				},
				"name": map[string]any{
					"type":        "string",
					"description": "Display name for the tab (optional).",
				},
				"init_message": map[string]any{
					"type":        "string",
					"description": "Initial message sent to the agent after startup (e.g., role instructions, domain context). The agent receives this as its first user input.",
				},
				"model": map[string]any{
					"type":        "string",
					"description": "Optional model override understood by the target provider.",
				},
				"workdir": map[string]any{
					"type":        "string",
					"description": "Optional working directory override for this session.",
				},
				"mcp_servers": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Optional per-session MCP servers to attach. 'nexus' is always included. Additional servers must be configured in external_mcp_servers.",
				},
				"context_packet": map[string]any{
					"type":        "object",
					"description": "Optional structured context for this session. Nexus injects it into startup instructions as the authoritative context packet.",
				},
			},
			Required: []string{"type"},
		},
	}, s.toolCreateSession)

	srv.AddTool(mcp.Tool{
		Name:        "destroy_session",
		Description: "Destroy an existing terminal session and close its tab. Use this to clean up test agents or stop stale peers.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"session_id": map[string]any{
					"type":        "string",
					"description": "Session ID to destroy.",
				},
			},
			Required: []string{"session_id"},
		},
	}, s.toolDestroySession)

	srv.AddTool(mcp.Tool{
		Name:        "get_time",
		Description: "Get the current system time.",
		InputSchema: mcp.ToolInputSchema{Type: "object", Properties: map[string]any{}},
	}, s.toolGetTime)

	srv.AddTool(mcp.Tool{
		Name:        "list_peers",
		Description: "List all active agent terminal sessions in the Nexus.",
		InputSchema: mcp.ToolInputSchema{Type: "object", Properties: map[string]any{}},
	}, s.toolListPeers)

	srv.AddTool(mcp.Tool{
		Name:        "ask_peer",
		Description: "Send a command to another terminal session and wait for its response.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"target":          map[string]any{"type": "string", "description": "Session ID of the target terminal."},
				"command":         map[string]any{"type": "string", "description": "Command to execute (newline appended automatically)."},
				"timeout_seconds": map[string]any{"type": "number", "description": "Max seconds to wait (default 30)."},
			},
			Required: []string{"target", "command"},
		},
	}, s.toolAskPeer)

	srv.AddTool(mcp.Tool{
		Name:        "broadcast",
		Description: "Send a command to ALL active terminals and collect their responses in parallel. Limited to 10 peers and 32KB total output.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"message":         map[string]any{"type": "string", "description": "Command/message to send to all peers (newline appended automatically)."},
				"timeout_seconds": map[string]any{"type": "number", "description": "Max seconds to wait per peer (default 30)."},
				"exclude":         map[string]any{"type": "string", "description": "Session ID to exclude (typically the caller's own session)."},
			},
			Required: []string{"message"},
		},
	}, s.toolBroadcast)

	// ── Profile management ──

	srv.AddTool(mcp.Tool{
		Name:        "list_profiles",
		Description: "List available agent profiles (declarative YAML role templates).",
		InputSchema: mcp.ToolInputSchema{Type: "object", Properties: map[string]any{}},
	}, s.toolListProfiles)

	srv.AddTool(mcp.Tool{
		Name:        "run_profile",
		Description: "Launch an agent from a profile template. Creates a session with preconfigured role, tools, and limits.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"name": map[string]any{"type": "string", "description": "Profile name to run."},
			},
			Required: []string{"name"},
		},
	}, s.toolRunProfile)

	srv.AddTool(mcp.Tool{
		Name:        "stop_profile",
		Description: "Stop a running profile session.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"name": map[string]any{"type": "string", "description": "Profile name to stop."},
			},
			Required: []string{"name"},
		},
	}, s.toolStopProfile)

	srv.AddTool(mcp.Tool{
		Name:        "read_peer",
		Description: "Read a peer's current terminal state. Returns status (idle/busy/error/dead), whether it's at a prompt, and the last N lines of output. MUST be called before send_to_peer (Read Guard).",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"target": map[string]any{"type": "string", "description": "Session ID of the target terminal."},
				"lines":  map[string]any{"type": "number", "description": "Number of lines to read (default 50)."},
				"caller": map[string]any{"type": "string", "description": "Your own session ID (for Read Guard tracking). If omitted, guard is tracked by target only."},
			},
			Required: []string{"target"},
		},
	}, s.toolReadPeer)

	srv.AddTool(mcp.Tool{
		Name:        "send_to_peer",
		Description: "Send text to another terminal's stdin (fire-and-forget, no response capture). Requires a prior read_peer call within 30s (Read Guard). Append \\r to execute.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"target":  map[string]any{"type": "string", "description": "Session ID of the target terminal."},
				"message": map[string]any{"type": "string", "description": "Text to send to stdin."},
				"caller":  map[string]any{"type": "string", "description": "Your own session ID (must match the caller used in read_peer)."},
			},
			Required: []string{"target", "message"},
		},
	}, s.toolSendToPeer)

	// ── Intent routing (powered by local Ollama) ──

	srv.AddTool(mcp.Tool{
		Name:        "classify_intent",
		Description: "Classify a user message to determine which capability should handle it. Uses local Ollama for fast (~1s) intent detection. Returns suggested module, confidence, and tool hint.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"message": map[string]any{"type": "string", "description": "User message or task description to classify."},
			},
			Required: []string{"message"},
		},
	}, s.toolClassifyIntent)
}

func (s *Server) toolClassifyIntent(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	message, _ := args["message"].(string)
	if message == "" {
		return errorResult("'message' is required"), nil
	}

	route := s.ClassifyIntent(ctx, message)
	if route == nil {
		return errorResult("Intent classification unavailable (Ollama/Gemma4 not running)"), nil
	}

	data, _ := json.MarshalIndent(route, "", "  ")
	return textResult(string(data)), nil
}

func (s *Server) toolCreateSession(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if err := checkRateLimit("create_session"); err != nil {
		return errorResult(err.Error()), nil
	}
	args := req.GetArguments()
	agentType, _ := args["type"].(string)
	name, _ := args["name"].(string)
	initMessage, _ := args["init_message"].(string)
	model, _ := args["model"].(string)
	workDir, _ := args["workdir"].(string)
	contextPacket := objectArg(args["context_packet"])
	mcpServers := stringSliceArg(args["mcp_servers"])
	initMessage = appendContextPacket(initMessage, contextPacket)
	initMessage = bootstrapInitMessage(agentType, initMessage)

	if agentType == "" {
		return errorResult("'type' is required (shell/claude/codex/gemini)."), nil
	}

	if s.createSession == nil {
		return errorResult("create_session not available (Nexus running in headless mode?)."), nil
	}

	sessionID, err := s.createSession(CreateSessionRequest{
		AgentType:     agentType,
		Name:          name,
		InitMessage:   initMessage,
		Model:         model,
		WorkDir:       workDir,
		MCPServers:    mcpServers,
		ContextPacket: contextPacket,
	})
	if err != nil {
		return errorResult(fmt.Sprintf("Failed to create session: %v", err)), nil
	}

	// Send init_message synchronously — block until prompt detected and message delivered.
	// This prevents race conditions where ask_peer is called before the agent is ready.
	if initMessage != "" && agentType != "shell" {
		sess := s.registry.Get(sessionID)
		if sess == nil {
			return errorResult(fmt.Sprintf("Session %q disappeared during init.", sessionID)), nil
		}

		// Wait for the agent CLI prompt.
		if ok := session.WaitForPrompt(sess, initMessageTimeout); !ok {
			log.Printf("[mcp] init_message: prompt not detected for %q within %v, sending anyway", sessionID, initMessageTimeout)
		}

		// Small delay after prompt detection to ensure the agent is fully ready.
		time.Sleep(300 * time.Millisecond)

		// Re-check session is still alive.
		sess = s.registry.Get(sessionID)
		if sess == nil {
			return errorResult(fmt.Sprintf("Session %q destroyed while waiting for prompt.", sessionID)), nil
		}

		// Split write: text first, then Enter separately (TUI compatibility).
		sess.PTY.Write([]byte(initMessage))
		time.Sleep(150 * time.Millisecond)
		sess.PTY.Write(session.EnterKey(sess.AgentType))
		log.Printf("[mcp] init_message sent to %q (%d chars)", sessionID, len(initMessage))
	}

	msg := fmt.Sprintf("Created %s terminal: session_id=%q. Init message delivered.", agentType, sessionID)
	if initMessage == "" {
		msg = fmt.Sprintf("Created %s terminal: session_id=%q.", agentType, sessionID)
	}

	// Add routing advice if Gemma4 is available and init_message describes a task.
	if initMessage != "" && s.ollama != nil {
		if route := s.ClassifyIntent(ctx, initMessage); route != nil && route.Confidence > 0.7 {
			msg += fmt.Sprintf("\n[routing] Suggested module: %s (confidence: %.0f%%)", route.Module, route.Confidence*100)
			if route.ToolHint != "" {
				msg += fmt.Sprintf(", try tool: %s", route.ToolHint)
			}
		}
	}

	return textResult(msg), nil
}

func (s *Server) toolDestroySession(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	sessionID, _ := args["session_id"].(string)
	if sessionID == "" {
		return errorResult("'session_id' is required."), nil
	}

	if s.destroySession == nil {
		return errorResult("destroy_session not available (Nexus service not wired)."), nil
	}

	if err := s.destroySession(sessionID); err != nil {
		return errorResult(fmt.Sprintf("Failed to destroy session: %v", err)), nil
	}

	return textResult(fmt.Sprintf("Destroyed session %q.", sessionID)), nil
}

func (s *Server) toolGetTime(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	now := time.Now().Format("2006-01-02 15:04:05 MST")
	return textResult(fmt.Sprintf("Current time: %s", now)), nil
}

func (s *Server) toolListPeers(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	peers := s.registry.ListDetails()
	if len(peers) == 0 {
		return textResult("No active sessions."), nil
	}
	data, _ := json.MarshalIndent(peers, "", "  ")
	return textResult(fmt.Sprintf("Active peers (%d):\n%s", len(peers), data)), nil
}

func (s *Server) toolAskPeer(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	target, _ := args["target"].(string)
	command, _ := args["command"].(string)
	timeoutSec, _ := args["timeout_seconds"].(float64)

	if target == "" || command == "" {
		return errorResult("Both 'target' and 'command' are required."), nil
	}

	sess := s.registry.Get(target)
	if sess == nil {
		return errorResult(fmt.Sprintf("Session %q not found.", target)), nil
	}

	timeout := defaultAskTimeout
	if timeoutSec > 0 {
		timeout = time.Duration(timeoutSec) * time.Second
	}
	askCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Use agent-level idle timeout for AI agents, shell timeout for shells.
	idle := s.idleTimeout
	if sess.AgentType != session.AgentShell {
		idle = s.agentIdleTimeout
	}

	// Gemini CLI needs \n; PowerShell/Claude use \r. Use \r\n for broad compatibility.
	cmd := command + "\r\n"
	output, err := session.CaptureOutput(askCtx, sess, cmd, idle)
	if err != nil {
		return errorResult(fmt.Sprintf("ask_peer failed: %v", err)), nil
	}
	if output == "" {
		output = "(no output captured)"
	}

	return textResult(fmt.Sprintf("Response from %s:\n%s", target, output)), nil
}

func (s *Server) toolBroadcast(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if err := checkRateLimit("broadcast"); err != nil {
		return errorResult(err.Error()), nil
	}
	args := req.GetArguments()
	message, _ := args["message"].(string)
	timeoutSec, _ := args["timeout_seconds"].(float64)
	exclude, _ := args["exclude"].(string)

	if message == "" {
		return errorResult("'message' is required."), nil
	}

	peers := s.registry.ListDetails()
	if len(peers) == 0 {
		return textResult("No active sessions to broadcast to."), nil
	}

	timeout := defaultAskTimeout
	if timeoutSec > 0 {
		timeout = time.Duration(timeoutSec) * time.Second
	}

	type result struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Output string `json:"output"`
		Error  string `json:"error,omitempty"`
	}

	var (
		mu      sync.Mutex
		results []result
		wg      sync.WaitGroup
	)

	cmd := message + "\r\n"
	peerCount := 0
	for _, p := range peers {
		if p.ID == exclude {
			continue
		}
		if peerCount >= maxBroadcastPeers {
			log.Printf("[broadcast] peer limit reached (%d), skipping remaining", maxBroadcastPeers)
			break
		}
		sess := s.registry.Get(p.ID)
		if sess == nil {
			continue
		}
		peerCount++
		wg.Add(1)
		go func(sess *session.Session, info session.SessionInfo) {
			defer wg.Done()
			bCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			idle := s.idleTimeout
			if sess.AgentType != session.AgentShell {
				idle = s.agentIdleTimeout
			}

			output, err := session.CaptureOutput(bCtx, sess, cmd, idle)
			r := result{ID: info.ID, Name: info.Name, Output: output}
			if err != nil {
				r.Error = err.Error()
			}
			if output == "" && r.Error == "" {
				r.Output = "(no output captured)"
			}
			mu.Lock()
			results = append(results, r)
			mu.Unlock()
		}(sess, p)
	}

	wg.Wait()

	// Build response with total output limit.
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Broadcast results (%d peers):\n\n", len(results)))
	totalLen := 0
	for _, r := range results {
		header := fmt.Sprintf("--- %s (%s) ---\n", r.Name, r.ID)
		sb.WriteString(header)
		totalLen += len(header)

		if r.Error != "" {
			errLine := fmt.Sprintf("ERROR: %s\n", r.Error)
			sb.WriteString(errLine)
			totalLen += len(errLine)
		}

		output := r.Output
		remaining := maxBroadcastOutput - totalLen
		if remaining <= 0 {
			sb.WriteString("... [output truncated due to total broadcast limit]\n\n")
			break
		}
		if len(output) > remaining {
			output = output[:remaining] + "\n... [truncated]"
		}
		sb.WriteString(output)
		sb.WriteString("\n\n")
		totalLen += len(output) + 2
	}

	return textResult(sb.String()), nil
}

func (s *Server) toolReadPeer(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	target, _ := args["target"].(string)
	lineCount, _ := args["lines"].(float64)
	caller, _ := args["caller"].(string)

	if target == "" {
		return errorResult("'target' is required."), nil
	}
	if caller == "" {
		caller = "_anonymous"
	}

	lines := int(lineCount)
	if lines <= 0 {
		lines = 50
	}

	result, err := s.registry.ReadPeer(caller, target, lines)
	if err != nil {
		return errorResult(fmt.Sprintf("read_peer failed: %v", err)), nil
	}

	data, _ := json.Marshal(result)
	return textResult(string(data)), nil
}

func (s *Server) toolSendToPeer(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	target, _ := args["target"].(string)
	message, _ := args["message"].(string)
	caller, _ := args["caller"].(string)
	if target == "" || message == "" {
		return errorResult("Both 'target' and 'message' are required."), nil
	}
	if caller == "" {
		caller = "_anonymous"
	}

	// Read Guard: caller must have called read_peer for this target within TTL.
	if err := s.registry.CheckGuard(caller, target); err != nil {
		return errorResult(fmt.Sprintf("Read Guard: %v", err)), nil
	}

	// Write the message text first (paste).
	if err := s.registry.WriteToSession(target, []byte(message)); err != nil {
		return errorResult(fmt.Sprintf("Failed to send: %v", err)), nil
	}
	// Wait for the terminal to process the pasted text before sending Enter.
	// ConPTY needs time to handle large pastes; without this delay the Enter
	// arrives before the text is fully buffered, causing the command to not execute.
	// Scale delay with message size: 150ms base + 50ms per KB.
	delay := 150 + len(message)/20 // ~50ms per KB
	if delay > 2000 {
		delay = 2000
	}
	time.Sleep(time.Duration(delay) * time.Millisecond)
	// Send agent-appropriate Enter key.
	sess := s.registry.Get(target)
	if sess != nil {
		s.registry.WriteToSession(target, session.EnterKey(sess.AgentType))
	} else {
		s.registry.WriteToSession(target, []byte("\r"))
	}

	// Clear guard after send — must read again before next send.
	s.registry.ClearGuard(caller, target)

	return textResult(fmt.Sprintf("Sent %d bytes + Enter to session %q.", len(message), target)), nil
}

func (s *Server) toolListProfiles(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if s.profileStore == nil {
		return textResult("No profile store configured."), nil
	}
	profiles := s.profileStore.List()
	if len(profiles) == 0 {
		return textResult("No profiles found. Add .yaml files to the nexus-profiles/ directory."), nil
	}
	data, _ := json.MarshalIndent(profiles, "", "  ")
	return textResult(fmt.Sprintf("Available profiles (%d):\n%s", len(profiles), data)), nil
}

func (s *Server) toolRunProfile(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	name, _ := args["name"].(string)

	if s.scheduler == nil {
		return errorResult("Scheduler not configured."), nil
	}

	sessionID, err := s.scheduler.RunProfile(name)
	if err != nil {
		return errorResult(fmt.Sprintf("Failed to run profile: %v", err)), nil
	}
	return textResult(fmt.Sprintf("Profile %q launched: session_id=%q", name, sessionID)), nil
}

func (s *Server) toolStopProfile(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	name, _ := args["name"].(string)

	if s.scheduler == nil {
		return errorResult("Scheduler not configured."), nil
	}

	if err := s.scheduler.StopProfile(name); err != nil {
		return errorResult(fmt.Sprintf("Failed to stop profile: %v", err)), nil
	}
	return textResult(fmt.Sprintf("Profile %q stopped.", name)), nil
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{mcp.TextContent{Type: "text", Text: text}},
	}
}

func errorResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{mcp.TextContent{Type: "text", Text: text}},
	}
}
