package session

import "testing"

func TestBestPeerViewPrefersLogTranscriptForCodex(t *testing.T) {
	snapshot := "◦Wo\n9% left · C:\\Projects\\myapp"
	logTail := "› 你好，请用一句话回复我，说明你已收到问候。\n• 已收到你的问候，来自 Nexus Codex。"

	got := bestPeerView(AgentCodex, snapshot, logTail)
	if got != logTail {
		t.Fatalf("bestPeerView() = %q, want %q", got, logTail)
	}
}

func TestPeerViewLinesUsesCodexTranscript(t *testing.T) {
	input := "◦Wo\n› 你好，请用一句话回复我，说明你已收到问候。\n• 已收到你的问候，来自 Nexus Codex。\n› Write tests for @filename\n  gpt-5.4 high · 90% left · C:\\Projects\\myapp"

	got := peerViewLines(AgentCodex, input)
	if len(got) != 2 {
		t.Fatalf("peerViewLines() len = %d, want 2; got %q", len(got), got)
	}
	if got[0] != "› 你好，请用一句话回复我，说明你已收到问候。" {
		t.Fatalf("peerViewLines()[0] = %q", got[0])
	}
	if got[1] != "• 已收到你的问候，来自 Nexus Codex。" {
		t.Fatalf("peerViewLines()[1] = %q", got[1])
	}
}

func TestBestPeerViewPrefersClaudeReplyTranscript(t *testing.T) {
	snapshot := "✶ Caramelizing…\n❯ "
	logTail := "❯ 你好，请只用一句中文回复。\n● 你好，已收到你的问候！\n"

	got := bestPeerView(AgentClaude, snapshot, logTail)
	if got != logTail {
		t.Fatalf("bestPeerView() = %q, want %q", got, logTail)
	}
}

func TestPeerViewLinesUsesClaudeTranscript(t *testing.T) {
	input := "✶ Caramelizing…\n❯ 你好，请只用一句中文回复。\n● 你好，已收到你的问候！\n⏵⏵ bypass permissions on (shift+tab to cycle)"

	got := peerViewLines(AgentClaude, input)
	if len(got) != 2 {
		t.Fatalf("peerViewLines() len = %d, want 2; got %q", len(got), got)
	}
	if got[0] != "❯ 你好，请只用一句中文回复。" {
		t.Fatalf("peerViewLines()[0] = %q", got[0])
	}
	if got[1] != "● 你好，已收到你的问候！" {
		t.Fatalf("peerViewLines()[1] = %q", got[1])
	}
}

func TestBestPeerViewPrefersGeminiReplyTranscript(t *testing.T) {
	snapshot := "workspace (/directory)\n⠴ Thinking... (esc to cancel, 0s)"
	logTail := "✦ 你好，我已经收到了你的问候。来自 Nexus Gemini\n\n? for shortcuts"

	got := bestPeerView(AgentGemini, snapshot, logTail)
	if got != logTail {
		t.Fatalf("bestPeerView() = %q, want %q", got, logTail)
	}
}

func TestPeerViewLinesUsesGeminiTranscript(t *testing.T) {
	input := "workspace (/directory)\n *   Type your message or @path/to/file\n✦ 你好，我已经收到了你的问候。来自 Nexus Gemini\n? for shortcuts"

	got := peerViewLines(AgentGemini, input)
	if len(got) != 1 {
		t.Fatalf("peerViewLines() len = %d, want 1; got %q", len(got), got)
	}
	if got[0] != "✦ 你好，我已经收到了你的问候。来自 Nexus Gemini" {
		t.Fatalf("peerViewLines()[0] = %q", got[0])
	}
}

func TestProviderAtPromptGeminiUsesLatestPromptMarker(t *testing.T) {
	text := "⠴ Thinking... (esc to cancel, 0s)\n✦ 你好，我已经收到你的问候，来自 Nexus Gemini。\n? for shortcuts"
	if !providerAtPrompt(AgentGemini, text) {
		t.Fatalf("providerAtPrompt() = false, want true")
	}
	if providerLooksBusy(AgentGemini, text) {
		t.Fatalf("providerLooksBusy() = true, want false")
	}
}

func TestProviderAtPromptClaudeUsesLatestPromptMarker(t *testing.T) {
	text := "✶ Caramelizing…\n● 你好，已收到你的问候！\n❯ "
	if !providerAtPrompt(AgentClaude, text) {
		t.Fatalf("providerAtPrompt(claude) = false, want true")
	}
	if providerLooksBusy(AgentClaude, text) {
		t.Fatalf("providerLooksBusy(claude) = true, want false")
	}
}

func TestProviderLooksBusyShellDefaultsIdleWithoutActiveCapture(t *testing.T) {
	text := "Windows PowerShell\nCopyright (C) Microsoft Corporation. All rights reserved.\n"
	if providerLooksBusy(AgentShell, text) {
		t.Fatalf("providerLooksBusy(shell) = true, want false")
	}
}
