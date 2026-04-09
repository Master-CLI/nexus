package mcp

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"
)

const intentClassifyPrompt = `You are a request router for a multi-agent platform. Given a user message, classify the intent to help route to the right capability.

Available capabilities:
- knowledge: Knowledge search, document retrieval, memory recall
- browser: Web browsing, page scraping, form filling
- discussion: Multi-agent discussion, consensus building
- nexus: Agent orchestration, peer communication
- code: Code analysis, file operations, git
- general: General question, no specific capability needed

Return ONLY a JSON object:
{"module": "knowledge|browser|discussion|nexus|code|general", "confidence": 0.0-1.0, "tool_hint": "suggested_tool_name or empty string"}

Keep it fast — this is a routing decision, not analysis.`

// IntentRoute holds the routing classification result.
type IntentRoute struct {
	Module     string  `json:"module"`
	Confidence float64 `json:"confidence"`
	ToolHint   string  `json:"tool_hint"`
}

// ClassifyIntent uses local Gemma4 to determine which module should handle a request.
// Returns nil if classification is unavailable.
func (s *Server) ClassifyIntent(ctx context.Context, userMessage string) *IntentRoute {
	if s.ollama == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	result, err := s.ollama.QuickClassify(ctx, intentClassifyPrompt, userMessage)
	if err != nil {
		log.Printf("[nexus-route] classify error: %v", err)
		return nil
	}

	result = strings.TrimSpace(result)
	result = strings.TrimPrefix(result, "```json")
	result = strings.TrimPrefix(result, "```")
	result = strings.TrimSuffix(result, "```")
	result = strings.TrimSpace(result)

	var route IntentRoute
	if err := json.Unmarshal([]byte(result), &route); err != nil {
		log.Printf("[nexus-route] parse error: %v (raw: %.200s)", err, result)
		return nil
	}

	log.Printf("[nexus-route] %q → module=%s confidence=%.2f hint=%s",
		userMessage[:min(len(userMessage), 60)], route.Module, route.Confidence, route.ToolHint)
	return &route
}

// SuggestAgentType maps a capability to the best agent type for handling it.
func (r *IntentRoute) SuggestAgentType() string {
	switch r.Module {
	case "code":
		return "claude"
	default:
		return "claude"
	}
}
