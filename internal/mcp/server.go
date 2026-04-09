package mcp

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/anthropic/nexus/internal/config"
	"github.com/anthropic/nexus/internal/session"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// CreateSessionRequest describes one session creation request from the MCP tool.
type CreateSessionRequest struct {
	AgentType     string
	Name          string
	InitMessage   string
	Model         string
	WorkDir       string
	MCPServers    []string
	ContextPacket map[string]any
}

// CreateSessionFunc creates a new terminal and returns its session ID.
// Used by the create_session MCP tool to delegate to the Wails service layer.
type CreateSessionFunc func(req CreateSessionRequest) (string, error)

// DestroySessionFunc closes an existing terminal session by ID.
// Used by the destroy_session MCP tool to delegate to the service layer.
type DestroySessionFunc func(sessionID string) error

// Server wraps the MCP server for Nexus.
type Server struct {
	mcpServer        *mcpserver.MCPServer
	registry         *session.Registry
	createSession    CreateSessionFunc
	destroySession   DestroySessionFunc
	profileStore     *session.ProfileStore
	scheduler        *session.Scheduler
	idleTimeout      time.Duration
	agentIdleTimeout time.Duration
	ready            chan struct{} // closed when registry is set
	bearerToken      string        // if non-empty, require Bearer auth
	ollama           *OllamaChat   // local Gemma4 for intent routing (optional)
}

// NewServer creates a new Nexus MCP server and registers tools/resources.
func NewServer(reg *session.Registry, cfg *config.Config) *Server {
	idleTimeout := 5 * time.Second
	agentIdleTimeout := 15 * time.Second
	if cfg != nil {
		if cfg.Capture.IdleTimeoutSec > 0 {
			idleTimeout = time.Duration(cfg.Capture.IdleTimeoutSec) * time.Second
		}
		if cfg.Capture.AgentIdleTimeoutSec > 0 {
			agentIdleTimeout = time.Duration(cfg.Capture.AgentIdleTimeoutSec) * time.Second
		}
	}

	var token string
	if cfg != nil && cfg.MCP.AuthEnabled {
		token = cfg.MCP.BearerToken
	}

	srv := &Server{
		registry:         reg,
		idleTimeout:      idleTimeout,
		agentIdleTimeout: agentIdleTimeout,
		ready:            make(chan struct{}),
		bearerToken:      token,
	}

	// Initialize local Ollama/Gemma4 for intent routing (optional).
	ollama := NewOllamaChat("", "gemma4")
	checkCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	if ollama.Available(checkCtx) {
		srv.ollama = ollama
		log.Printf("[mcp] intent routing enabled: Ollama/gemma4")
	} else {
		log.Printf("[mcp] Ollama not available — intent routing disabled")
	}
	cancel()

	// If registry is already provided, mark as ready immediately.
	if reg != nil {
		close(srv.ready)
	}

	// Register custom prompt patterns if configured.
	if cfg != nil && len(cfg.Capture.ExtraPromptPatterns) > 0 {
		session.RegisterExtraPromptPatterns(cfg.Capture.ExtraPromptPatterns)
	}

	instructions := "You are connected to Nexus — a multi-agent terminal orchestration hub.\n\n" +
		"## Tools\n" +
		"- create_session(type, name?, init_message?): Spawn a new agent terminal in Nexus\n" +
		"- destroy_session(session_id): Close an existing terminal session\n" +
		"- list_peers: List all active terminal sessions\n" +
		"- read_peer(target, lines?, caller?): Read a peer's terminal state (status, last lines, prompt detection)\n" +
		"- ask_peer(target, command): Send a question to a peer and get its response\n" +
		"- broadcast(message): Fan-out to all peers, collect responses\n" +
		"- send_to_peer(target, message, caller?): Fire-and-forget stdin injection (requires read_peer first)\n" +
		"- list_profiles: List available agent profiles\n" +
		"- run_profile(name): Launch an agent from a profile template\n\n" +
		"## Read Guard\n" +
		"Before using send_to_peer, you MUST call read_peer on the target first.\n" +
		"This prevents blind input — you should observe a peer's state before writing to it.\n" +
		"- read_peer returns: status (idle/busy/error/dead), at_prompt flag, last N lines\n" +
		"- The guard expires after 30 seconds — call read_peer again if too much time passes\n" +
		"- After each send_to_peer, the guard is cleared — read again before the next send\n" +
		"- ask_peer does NOT require a prior read_peer (it captures output automatically)\n" +
		"- Pass your own session ID as 'caller' in both read_peer and send_to_peer for tracking\n\n" +
		"## Delegation Strategy\n" +
		"When a user's question requires deep domain expertise, RAG knowledge research,\n" +
		"or a second AI perspective, you SHOULD autonomously create a specialist agent:\n\n" +
		"1. create_session(type=\"claude\", name=\"math-expert\",\n" +
		"     init_message=\"You are a math expert. Use rag_search(domains=['math']) to research and answer questions.\")\n" +
		"2. ask_peer(target=<new_session_id>, command=\"<user's question>\")\n" +
		"3. Synthesize the specialist's response and reply to the user.\n\n" +
		"Good reasons to delegate:\n" +
		"- Question needs RAG knowledge base research (create agent with rag_search instructions)\n" +
		"- Want a different AI model's perspective (create codex or gemini agent)\n" +
		"- Task requires extended autonomous exploration (agent works in background)\n" +
		"- Domain-specific analysis (code review, security audit, architecture design)\n\n" +
		"Do NOT delegate for simple questions you can answer directly.\n\n" +
		"## Limits\n" +
		"- Max sessions: controlled by config (default 8). create_session fails if limit reached.\n" +
		"- Broadcast limited to 10 peers and 32KB total output.\n" +
		"- If YOU were created by another agent (you received an init_message with a role),\n" +
		"  do NOT create further agents. Focus on your assigned task and respond directly."

	mcpSrv := mcpserver.NewMCPServer(
		"Nexus MCP Server",
		"1.0.0",
		mcpserver.WithResourceCapabilities(true, true),
		mcpserver.WithToolCapabilities(true),
		mcpserver.WithInstructions(instructions),
	)

	srv.registerTools(mcpSrv)
	srv.registerResources(mcpSrv)

	srv.mcpServer = mcpSrv
	return srv
}

// BearerToken returns the auth token (for writing into .mcp.json).
func (s *Server) BearerToken() string {
	return s.bearerToken
}

// SetRegistry wires the session registry (may be set after construction).
func (s *Server) SetRegistry(reg *session.Registry) {
	s.registry = reg
	select {
	case <-s.ready:
	default:
		close(s.ready)
	}
}

// SetCreateSession wires the terminal creation callback.
func (s *Server) SetCreateSession(fn CreateSessionFunc) {
	s.createSession = fn
}

// SetDestroySession wires the terminal destruction callback.
func (s *Server) SetDestroySession(fn DestroySessionFunc) {
	s.destroySession = fn
}

// SetProfileStore wires the profile store and scheduler.
func (s *Server) SetProfileStore(ps *session.ProfileStore) {
	s.profileStore = ps
}

// SetScheduler wires the scheduler.
func (s *Server) SetScheduler(sched *session.Scheduler) {
	s.scheduler = sched
}

// Listen starts the MCP server on the given port.
// Blocks until registry is set (via SetRegistry) before accepting connections.
func (s *Server) Listen(ctx context.Context, port int) {
	// Wait for registry to be wired before accepting connections.
	select {
	case <-s.ready:
	case <-ctx.Done():
		return
	}

	addr := fmt.Sprintf(":%d", port)

	sseHandler := mcpserver.NewSSEServer(s.mcpServer)
	streamHandler := mcpserver.NewStreamableHTTPServer(s.mcpServer)

	mux := http.NewServeMux()
	mux.Handle("/sse", s.authMiddleware(sseHandler))
	mux.Handle("/message", s.authMiddleware(sseHandler))
	mux.Handle("/mcp", s.authMiddleware(streamHandler))

	srv := &http.Server{Addr: addr, Handler: mux}

	log.Printf("[mcp] Nexus MCP server listening on %s (SSE: /sse, Streamable HTTP: /mcp)", addr)
	if s.bearerToken != "" {
		log.Printf("[mcp] Bearer token auth enabled")
	}

	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("[mcp] server error: %v", err)
	}
}

// authMiddleware checks Bearer token if auth is enabled.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	if s.bearerToken == "" {
		return next // no auth
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "" {
			// Also check query param for SSE (browsers can't set headers on EventSource).
			auth = "Bearer " + r.URL.Query().Get("token")
		}
		expected := "Bearer " + s.bearerToken
		if !strings.EqualFold(auth, expected) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
