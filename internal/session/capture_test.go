package session

import (
	"strings"
	"testing"
)

func TestClaudeAssistantReplyStripsTUI(t *testing.T) {
	raw := "an…❯ 你好，请只用一句中文回复，说明你已收到问候，不要提及论坛、角色、身份或平台。\n" +
		"\n" +
		"✶ Caramelizing…\n" +
		"─────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────\n" +
		"❯ \n" +
		"●你好，已收到你的问候！✻ Caramelizing…                                                                                                             ❯ \n" +
		"─────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────❯    ⏵⏵ bypass permissions on (shift+tab to cycle)6 MCP servers failed · /mcp\n" +
		"\n" +
		" ▐▛███▜▌   Claude Code v2.1.92\n" +
		"▝▜█████▛▘  Sonnet 4.6 with high effort · Claude Max\n" +
		"  ▘▘ ▝▝    C:\\Projects\\myapp\n" +
		"\n" +
		"❯ 你好，请只用一句中文回复，说明你已收到问候，不要提及论坛、角色、身份或平台。\n" +
		"\n" +
		"● 你好，已收到你的问候！\n" +
		"\n" +
		"─────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────\n" +
		"  ⏵⏵ bypass permissions on (shift+tab to cycle)\n"

	clean := filterAgentNoise(AgentClaude, raw)
	got := claudeAssistantReply(clean)
	want := "● 你好，已收到你的问候！"
	if got != want {
		t.Fatalf("claudeAssistantReply() = %q, want %q", got, want)
	}
	if strings.Contains(clean, "Caramelizing") {
		t.Fatalf("filterAgentNoise() kept Claude thinking noise: %q", clean)
	}
}

func TestFinalizeOutputStripsCodexPromptBlock(t *testing.T) {
	raw := "你好，请用一句话回复我，说明你已收到问候。\r\n" +
		"• 已收到你的问候，来自 Nexus Codex。\r\n" +
		"› Write tests for @filename\r\n" +
		"  gpt-5.4 high · 90% left · C:\\Projects\\myapp\r\n"

	got := finalizeOutput(AgentCodex, raw, "你好，请用一句话回复我，说明你已收到问候。")
	want := "• 已收到你的问候，来自 Nexus Codex。"
	if got != want {
		t.Fatalf("finalizeOutput() = %q, want %q", got, want)
	}
}

func TestFilterAgentNoiseDropsCodexSpinnerFragments(t *testing.T) {
	raw := "Wo\nor\nrk\n" +
		"◦ Working (2s • esc to interrupt)\n" +
		"• 第二次问候收到，来自 Nexus Codex。\n" +
		"› Write tests for @filename\n" +
		"  gpt-5.4 high · 90% left · C:\\Projects\\myapp\n"

	got := filterAgentNoise(AgentCodex, raw)
	want := "• 第二次问候收到，来自 Nexus Codex。"
	if got != want {
		t.Fatalf("filterAgentNoise() = %q, want %q", got, want)
	}
}

func TestCodexAssistantReplyReturnsLatestAssistantBlock(t *testing.T) {
	raw := "› 你好，请用一句话回复我，说明你已收到问候。\n" +
		"• 已收到你的问候，来自 Nexus Codex。\n" +
		"Use /skills to list available skills\n" +
		"› 再次测试：请只回复第二次问候收到，来自 Nexus Codex。\n" +
		"• 第二次问候收到，来自 Nexus Codex。\n" +
		"› Write tests for @filename\n" +
		"  gpt-5.4 high · 90% left · C:\\Projects\\myapp\n"

	got := codexAssistantReply(filterAgentNoise(AgentCodex, raw))
	want := "• 第二次问候收到，来自 Nexus Codex。"
	if got != want {
		t.Fatalf("codexAssistantReply() = %q, want %q", got, want)
	}
}

func TestGeminiAssistantReplyStripsTUI(t *testing.T) {
	raw := "▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄\n" +
		" workspace (/directory)                                                sandbox                                    /model\n" +
		" ~\\AppData\\Local\\Temp\\nexus-launch\\gemini-probe no sandbox Auto (Gemini 3)\n" +
		"\n" +
		" ⠴ Thinking... (esc to cancel, 0s)              Tip: Show citations to see where the model gets information (/settings)…\n" +
		"─────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────\n" +
		" YOLO Ctrl+Y                                                                  1 GEMINI.md file · 1 MCP server · 2 skills\n" +
		"▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀\n" +
		" *   Type your message or @path/to/file\n" +
		"✦ 你好，我已经收到了你的问候。来自 Nexus Gemini\n" +
		"\n" +
		"? for shortcuts\n"

	clean := filterAgentNoise(AgentGemini, raw)
	got := geminiAssistantReply(clean)
	want := "✦ 你好，我已经收到了你的问候。来自 Nexus Gemini"
	if got != want {
		t.Fatalf("geminiAssistantReply() = %q, want %q", got, want)
	}
}

func TestSanitizeGeminiReplyLineStripsInlinePromptHints(t *testing.T) {
	input := "✦ 你好，我已经收到你的问候，来自 Nexus Gemini。? for shortcuts"
	got := sanitizeGeminiReplyLine(input)
	want := "✦ 你好，我已经收到你的问候，来自 Nexus Gemini。"
	if got != want {
		t.Fatalf("sanitizeGeminiReplyLine() = %q, want %q", got, want)
	}
}

func TestGeminiAssistantReplyIgnoresQueuedTail(t *testing.T) {
	raw := "✦ 已收到你的问候，来自 Nexus Gemini\n" +
		"Queued (press ↑ to edit):\n" +
		"你好，请用一句话回复我。\n" +
		"? for shortcuts\n"

	clean := filterAgentNoise(AgentGemini, raw)
	got := geminiAssistantReply(clean)
	want := "✦ 已收到你的问候，来自 Nexus Gemini"
	if got != want {
		t.Fatalf("geminiAssistantReply() = %q, want %q", got, want)
	}
}
