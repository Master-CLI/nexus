package mcp

import (
	"strings"
	"testing"
)

func TestBootstrapInitMessageAddsNeutralSessionPreamble(t *testing.T) {
	got := bootstrapInitMessage("gemini", "You are a test agent.")
	for _, want := range []string{
		"Treat this Nexus-started session as fresh and blank at startup.",
		"Do not proactively summarize the repository",
		"ignore incidental repository context",
		"You are a test agent.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("bootstrapInitMessage() missing %q:\n%s", want, got)
		}
	}
}

func TestBootstrapInitMessageLeavesShellUntouched(t *testing.T) {
	if got := bootstrapInitMessage("shell", "hello"); got != "hello" {
		t.Fatalf("shell init message = %q, want unchanged", got)
	}
}

func TestAppendContextPacketPrependsStructuredContext(t *testing.T) {
	got := appendContextPacket("Reply in one sentence.", map[string]any{
		"topic_id": "topic-123",
		"round":    1,
		"mode":     "independent",
	})
	for _, want := range []string{
		"Session context packet",
		`"topic_id": "topic-123"`,
		`"round": 1`,
		"Reply in one sentence.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("appendContextPacket() missing %q:\n%s", want, got)
		}
	}
}
