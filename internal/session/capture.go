package session

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"
)

const maxOutputChars = 8000            // truncate captured output beyond this
const maxCaptureTime = 2 * time.Minute // hard upper limit for any capture

// EnterKey returns the appropriate Enter sequence for a given agent type.
// All CLI agents use \r (carriage return) — matches ConPTY Enter behavior on Windows.
func EnterKey(agentType AgentType) []byte {
	return []byte("\r")
}

// Common shell and agent prompt patterns (end of line).
var promptPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?m)^PS [A-Z]:\\[^>]*> $`),                    // PowerShell
	regexp.MustCompile(`(?m)^\$ $`),                                   // bash minimal
	regexp.MustCompile(`(?m)^> $`),                                    // generic / Gemini
	regexp.MustCompile(`(?m)^[a-zA-Z0-9_-]+@[a-zA-Z0-9_-]+.*[\$#] $`), // user@host$
	regexp.MustCompile(`(?m)^[A-Z]:\\[^>]*>$`),                        // cmd.exe
	regexp.MustCompile(`(?m)^❯ ?$`),                                   // Claude Code prompt
	regexp.MustCompile(`(?m)^› $`),                                    // Claude Code alternate
	regexp.MustCompile(`(?m)❯❯ .+$`),                                  // Claude Code v2+ with status text
	regexp.MustCompile(`(?m)^codex> $`),                               // Codex prompt (legacy)
	regexp.MustCompile(`(?ms)(?:^|\n)› .+\n(?:\n)?  gpt-\d+\.\d+.*$`), // Codex prompt + status block
}

var (
	claudePromptLineRe   = regexp.MustCompile(`^❯(?:\s+.*)?$`)
	claudeReplyLineRe    = regexp.MustCompile(`^●\s*.+$`)
	claudeThinkingLineRe = regexp.MustCompile(`^[✶✻✢]\s+.+(?:…|\.{3})$`)
	claudeBorderLineRe   = regexp.MustCompile(`^[─]+$`)
	claudeHeaderLineRe   = regexp.MustCompile(`^(?:▐▛███▜▌|▝▜█████▛▘|▘▘\s*▝▝|Claude Code v\d|Sonnet\b|Opus\b|Haiku\b).*$`)
	claudeControlLineRe  = regexp.MustCompile(`^(?:⏵⏵ .+|●\s*high.*\/effort.*|\d+\s*MCP servers failed\s*·\s*/mcp.*|You've used \d+% of your weekly limit.*)$`)
	claudeInlineTailRe   = regexp.MustCompile(`\s(?:[\*✶✻✢]\s+[A-Za-z][A-Za-z -]*(?:…|\.{3})).*$`)
	codexPromptLineRe    = regexp.MustCompile(`^› .+`)
	codexStatusLineRe    = regexp.MustCompile(`^gpt-\d+\.\d+ .*left.*$`)
	codexWorkingLineRe   = regexp.MustCompile(`^[•◦] Working\b.*$`)
	codexTipLineRe       = regexp.MustCompile(`^(?:› )?Use /skills\b.*$`)
	geminiPromptLineRe   = regexp.MustCompile(`^\*\s+Type your message or @path/to/file$`)
	geminiShortcutsRe    = regexp.MustCompile(`^\? for shortcuts$`)
	geminiStatusLineRe   = regexp.MustCompile(`^[⠁-⣿]\s+(?:Thinking|Processing|Analyzing|Formulating)\b.*$`)
	geminiReplyLineRe    = regexp.MustCompile(`^✦\s+.+$`)
	geminiTipLineRe      = regexp.MustCompile(`^Tip: .+$`)
	geminiYoloLineRe     = regexp.MustCompile(`^YOLO Ctrl\+Y.+$`)
	geminiQueuedLineRe   = regexp.MustCompile(`^Queued \(press .+\):$`)
	geminiWorkspaceRe    = regexp.MustCompile(`^workspace \(/directory\).+sandbox.+/model$`)
	geminiPathLineRe     = regexp.MustCompile(`^(?:~\\|[A-Z]:\\).*\(Gemini 3\)$`)
	geminiBorderLineRe   = regexp.MustCompile(`^[▄▀─]+$`)
	geminiBannerLineRe   = regexp.MustCompile(`^\d+\s+GEMINI\.md file\b.*$`)
	shortASCIIRe         = regexp.MustCompile(`^[A-Za-z0-9]{1,3}$`)
)

// RegisterExtraPromptPatterns adds user-defined prompt regex patterns from config.
func RegisterExtraPromptPatterns(patterns []string) {
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			continue
		}
		promptPatterns = append(promptPatterns, re)
	}
}

// endsWithPrompt returns true if the accumulated output ends with a shell prompt.
func endsWithPrompt(output string) bool {
	tail := output
	if len(tail) > 300 {
		tail = tail[len(tail)-300:]
	}
	for _, re := range promptPatterns {
		if re.MatchString(tail) {
			return true
		}
	}
	return false
}

// stripANSI removes ANSI escape sequences.
var ansiRe = regexp.MustCompile(
	`\x1b\[[0-9;]*[A-Za-z]` +
		`|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)` +
		`|\x1b\[[\?]?[0-9;]*[hl]` +
		`|\x1b[PX][^\x1b]*\x1b\\` +
		`|\x1b\([AB012]` +
		`|\x1b[=>]` +
		`|\x1b\[[?>=][0-9;]*[a-zA-Z]`, // terminal capability queries ([?u, [>q, etc.)
)

func stripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

func normalizeTerminalText(s string) string {
	s = stripANSI(s)
	runes := []rune(s)

	var (
		lines []string
		line  []rune
	)
	flush := func() {
		lines = append(lines, string(line))
		line = line[:0]
	}

	for i, r := range runes {
		switch r {
		case '\r':
			nextIsLF := i+1 < len(runes) && runes[i+1] == '\n'
			if nextIsLF {
				continue
			}
			line = line[:0]
		case '\n':
			flush()
		case '\b':
			if len(line) > 0 {
				line = line[:len(line)-1]
			}
		case '\t':
			line = append(line, r)
		default:
			if r < 0x20 {
				continue
			}
			line = append(line, r)
		}
	}

	if len(line) > 0 {
		lines = append(lines, string(line))
	}

	return strings.Join(lines, "\n")
}

func collapseBlankLines(lines []string) string {
	var out []string
	prevBlank := false
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			if !prevBlank {
				out = append(out, "")
			}
			prevBlank = true
			continue
		}
		out = append(out, line)
		prevBlank = false
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func filterAgentNoise(agentType AgentType, text string) string {
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	switch agentType {
	case AgentClaude:
		return filterClaudeNoise(lines)
	case AgentCodex:
		return filterCodexNoise(lines)
	case AgentGemini:
		return filterGeminiNoise(lines)
	default:
		return collapseBlankLines(lines)
	}
}

func filterClaudeNoise(lines []string) string {
	filtered := make([]string, 0, len(lines))
	appendBreak := func() {
		if len(filtered) == 0 || filtered[len(filtered)-1] == "" {
			return
		}
		filtered = append(filtered, "")
	}
	for _, raw := range lines {
		line := sanitizeClaudeLine(raw)
		compact := strings.TrimSpace(line)
		switch {
		case compact == "":
			appendBreak()
		case isClaudeUILine(compact):
			appendBreak()
		default:
			filtered = append(filtered, compact)
		}
	}
	return collapseBlankLines(filtered)
}

func filterCodexNoise(lines []string) string {
	codexUISeen := false
	for i, line := range lines {
		compact := strings.TrimSpace(line)
		if isCodexPromptLine(lines, i) || codexStatusLineRe.MatchString(compact) || codexWorkingLineRe.MatchString(compact) {
			codexUISeen = true
			break
		}
	}

	filtered := make([]string, 0, len(lines))
	for i, line := range lines {
		compact := strings.TrimSpace(line)
		switch {
		case compact == "":
			filtered = append(filtered, "")
		case isCodexPromptLine(lines, i):
			continue
		case codexStatusLineRe.MatchString(compact):
			continue
		case codexWorkingLineRe.MatchString(compact):
			continue
		case codexUISeen && shortASCIIRe.MatchString(compact):
			continue
		default:
			filtered = append(filtered, line)
		}
	}

	return collapseBlankLines(filtered)
}

func filterGeminiNoise(lines []string) string {
	filtered := make([]string, 0, len(lines))
	appendBreak := func() {
		if len(filtered) == 0 || filtered[len(filtered)-1] == "" {
			return
		}
		filtered = append(filtered, "")
	}
	for _, line := range lines {
		compact := strings.TrimSpace(line)
		switch {
		case compact == "":
			filtered = append(filtered, "")
		case geminiBorderLineRe.MatchString(compact):
			appendBreak()
		case geminiPromptLineRe.MatchString(compact):
			appendBreak()
		case geminiShortcutsRe.MatchString(compact):
			appendBreak()
		case geminiStatusLineRe.MatchString(compact):
			appendBreak()
		case geminiTipLineRe.MatchString(compact):
			appendBreak()
		case geminiYoloLineRe.MatchString(compact):
			appendBreak()
		case geminiQueuedLineRe.MatchString(compact):
			appendBreak()
		case geminiWorkspaceRe.MatchString(compact):
			appendBreak()
		case geminiPathLineRe.MatchString(compact):
			appendBreak()
		case geminiBannerLineRe.MatchString(compact):
			appendBreak()
		default:
			filtered = append(filtered, line)
		}
	}
	return collapseBlankLines(filtered)
}

func claudeTranscriptLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	var out []string
	inAssistantBlock := false

	for _, raw := range lines {
		line := sanitizeClaudeLine(raw)
		switch {
		case line == "":
			if inAssistantBlock && len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
			inAssistantBlock = false
		case isClaudeUILine(line):
			inAssistantBlock = false
		case claudeReplyLineRe.MatchString(line):
			out = append(out, line)
			inAssistantBlock = true
		case claudePromptLineRe.MatchString(line):
			out = append(out, line)
			inAssistantBlock = false
		case inAssistantBlock:
			out = append(out, line)
		}
	}

	return out
}

func claudeAssistantReply(text string) string {
	lines := claudeTranscriptLines(text)
	if len(lines) == 0 {
		return ""
	}

	var (
		current []string
		blocks  []string
	)
	flush := func() {
		if len(current) == 0 {
			return
		}
		blocks = append(blocks, strings.Join(current, "\n"))
		current = nil
	}

	for _, line := range lines {
		switch {
		case line == "":
			flush()
		case claudePromptLineRe.MatchString(line):
			flush()
		case claudeReplyLineRe.MatchString(line):
			flush()
			current = append(current, line)
		default:
			if len(current) > 0 {
				current = append(current, line)
			}
		}
	}
	flush()

	if len(blocks) == 0 {
		return ""
	}
	return strings.TrimSpace(blocks[len(blocks)-1])
}

func codexTranscriptLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	var out []string
	inAssistantBlock := false

	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		switch {
		case line == "":
			if inAssistantBlock && len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
			inAssistantBlock = false
		case isCodexPromptLine(lines, i):
			inAssistantBlock = false
		case codexStatusLineRe.MatchString(line):
			inAssistantBlock = false
		case codexWorkingLineRe.MatchString(line):
			inAssistantBlock = false
		case codexTipLineRe.MatchString(line):
			inAssistantBlock = false
		case shortASCIIRe.MatchString(line):
			continue
		case strings.HasPrefix(line, "• "):
			out = append(out, line)
			inAssistantBlock = true
		case strings.HasPrefix(line, "› "):
			out = append(out, line)
			inAssistantBlock = false
		case inAssistantBlock:
			out = append(out, line)
		}
	}

	return out
}

func isCodexPromptLine(lines []string, idx int) bool {
	line := strings.TrimSpace(lines[idx])
	if !codexPromptLineRe.MatchString(line) {
		return false
	}
	for i := idx + 1; i < len(lines); i++ {
		next := strings.TrimSpace(lines[i])
		if next == "" {
			continue
		}
		return codexStatusLineRe.MatchString(next)
	}
	return false
}

func codexAssistantReply(text string) string {
	lines := codexTranscriptLines(text)
	if len(lines) == 0 {
		return ""
	}

	var (
		current []string
		blocks  []string
	)
	flush := func() {
		if len(current) == 0 {
			return
		}
		blocks = append(blocks, strings.Join(current, "\n"))
		current = nil
	}

	for _, line := range lines {
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "› "):
			flush()
		case strings.HasPrefix(line, "• "):
			flush()
			current = append(current, line)
		default:
			if len(current) > 0 {
				current = append(current, line)
			}
		}
	}
	flush()

	if len(blocks) == 0 {
		return ""
	}
	return strings.TrimSpace(blocks[len(blocks)-1])
}

func geminiTranscriptLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	var out []string
	inReply := false

	for _, raw := range lines {
		line := sanitizeGeminiReplyLine(strings.TrimSpace(raw))
		switch {
		case line == "":
			if inReply && len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
			inReply = false
		case geminiReplyLineRe.MatchString(line):
			out = append(out, line)
			inReply = true
		case inReply && !isGeminiUILine(line):
			out = append(out, line)
		default:
			inReply = false
		}
	}

	return out
}

func geminiAssistantReply(text string) string {
	lines := geminiTranscriptLines(text)
	if len(lines) == 0 {
		return ""
	}

	var (
		current []string
		blocks  []string
	)
	flush := func() {
		if len(current) == 0 {
			return
		}
		blocks = append(blocks, strings.Join(current, "\n"))
		current = nil
	}

	for _, line := range lines {
		switch {
		case line == "":
			flush()
		case geminiReplyLineRe.MatchString(strings.TrimSpace(line)):
			flush()
			current = append(current, strings.TrimSpace(line))
		default:
			if len(current) > 0 {
				current = append(current, strings.TrimSpace(line))
			}
		}
	}
	flush()

	if len(blocks) == 0 {
		return ""
	}
	return strings.TrimSpace(blocks[len(blocks)-1])
}

func sanitizeClaudeLine(line string) string {
	line = strings.ReplaceAll(line, "\u00a0", " ")
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	if strings.HasPrefix(line, "─") {
		return ""
	}

	if idx := strings.Index(line, "●"); idx >= 0 {
		line = strings.TrimSpace(line[idx:])
	} else if idx := strings.Index(line, "❯"); idx >= 0 {
		line = strings.TrimSpace(line[idx:])
	}

	for _, marker := range []string{"✶ ", "✻ ", "✢ ", "❯ ", "⏵⏵ ", "You've used ", "6 MCP servers failed · /mcp"} {
		if idx := strings.Index(line, marker); idx > 0 {
			line = strings.TrimSpace(line[:idx])
		}
	}

	if strings.HasPrefix(line, "●") && !strings.HasPrefix(line, "● ") {
		line = "● " + strings.TrimSpace(strings.TrimPrefix(line, "●"))
	}
	line = strings.TrimSpace(claudeInlineTailRe.ReplaceAllString(line, ""))
	return strings.TrimSpace(line)
}

func sanitizeGeminiReplyLine(line string) string {
	for _, marker := range []string{"? for shortcuts", "Tip: "} {
		if idx := strings.Index(line, marker); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
		}
	}
	return strings.TrimSpace(line)
}

func isClaudeUILine(line string) bool {
	switch {
	case claudeBorderLineRe.MatchString(line):
		return true
	case claudeHeaderLineRe.MatchString(line):
		return true
	case claudeThinkingLineRe.MatchString(line):
		return true
	case claudeControlLineRe.MatchString(line):
		return true
	default:
		return false
	}
}

func isGeminiUILine(line string) bool {
	switch {
	case geminiBorderLineRe.MatchString(line):
		return true
	case geminiPromptLineRe.MatchString(line):
		return true
	case geminiShortcutsRe.MatchString(line):
		return true
	case geminiStatusLineRe.MatchString(line):
		return true
	case geminiTipLineRe.MatchString(line):
		return true
	case geminiYoloLineRe.MatchString(line):
		return true
	case geminiQueuedLineRe.MatchString(line):
		return true
	case geminiWorkspaceRe.MatchString(line):
		return true
	case geminiPathLineRe.MatchString(line):
		return true
	case geminiBannerLineRe.MatchString(line):
		return true
	default:
		return false
	}
}

func providerReply(agentType AgentType, text string) string {
	switch agentType {
	case AgentClaude:
		return claudeAssistantReply(text)
	case AgentCodex:
		return codexAssistantReply(text)
	case AgentGemini:
		return geminiAssistantReply(text)
	default:
		return ""
	}
}

func providerTranscriptLines(agentType AgentType, text string) []string {
	switch agentType {
	case AgentClaude:
		return claudeTranscriptLines(text)
	case AgentCodex:
		return codexTranscriptLines(text)
	case AgentGemini:
		return geminiTranscriptLines(text)
	default:
		return nil
	}
}

func providerAtPrompt(agentType AgentType, text string) bool {
	switch agentType {
	case AgentClaude:
		tail := tailText(text, 4000)
		lastPrompt := lastProviderMarkerIndex(tail, func(line string) bool {
			return claudePromptLineRe.MatchString(sanitizeClaudeLine(line))
		})
		lastBusy := lastProviderMarkerIndex(tail, func(line string) bool {
			s := sanitizeClaudeLine(line)
			return claudeThinkingLineRe.MatchString(s) || claudeControlLineRe.MatchString(s)
		})
		return lastPrompt >= 0 && lastPrompt > lastBusy
	case AgentGemini:
		tail := tailText(text, 4000)
		lastPrompt := lastProviderMarkerIndex(tail, func(line string) bool {
			return geminiPromptLineRe.MatchString(line) || strings.Contains(line, "? for shortcuts")
		})
		lastBusy := lastProviderMarkerIndex(tail, func(line string) bool {
			return geminiStatusLineRe.MatchString(line)
		})
		return lastPrompt >= 0 && lastPrompt > lastBusy
	default:
		return endsWithPrompt(text)
	}
}

func providerLooksBusy(agentType AgentType, text string) bool {
	switch agentType {
	case AgentShell:
		return false
	case AgentClaude:
		tail := tailText(text, 4000)
		lastBusy := lastProviderMarkerIndex(tail, func(line string) bool {
			s := sanitizeClaudeLine(line)
			return claudeThinkingLineRe.MatchString(s) || claudeControlLineRe.MatchString(s)
		})
		lastPrompt := lastProviderMarkerIndex(tail, func(line string) bool {
			return claudePromptLineRe.MatchString(sanitizeClaudeLine(line))
		})
		return lastBusy > lastPrompt
	case AgentGemini:
		tail := tailText(text, 4000)
		lastBusy := lastProviderMarkerIndex(tail, func(line string) bool {
			return geminiStatusLineRe.MatchString(line)
		})
		lastPrompt := lastProviderMarkerIndex(tail, func(line string) bool {
			return geminiPromptLineRe.MatchString(line) || strings.Contains(line, "? for shortcuts")
		})
		return lastBusy > lastPrompt
	default:
		return !providerAtPrompt(agentType, text)
	}
}

func tailText(text string, maxChars int) string {
	if len(text) <= maxChars {
		return text
	}
	return text[len(text)-maxChars:]
}

func lastProviderMarkerIndex(text string, match func(string) bool) int {
	lines := strings.Split(text, "\n")
	offset := 0
	last := -1
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if match(line) {
			last = offset
		}
		offset += len(raw) + 1
	}
	return last
}

// stripTUIArtifacts aggressively cleans TUI rendering noise:
// - Remove all ANSI escape sequences
// - Remove control characters (backspace, carriage return overwrite, BEL, etc.)
// - Collapse consecutive blank lines into one
// - Trim each line
func stripTUIArtifacts(s string) string {
	return collapseBlankLines(strings.Split(normalizeTerminalText(s), "\n"))
}

// StripANSI removes ANSI escape sequences (exported for use by other packages).
func StripANSI(s string) string { return stripANSI(s) }

// StripTUIArtifacts aggressively cleans TUI noise (exported for use by other packages).
func StripTUIArtifacts(s string) string { return stripTUIArtifacts(s) }

// WaitForPrompt polls session output until a shell/agent prompt is detected.
// Returns true if prompt was found, false on timeout.
func WaitForPrompt(s *Session, timeout time.Duration) bool {
	ch := s.StartCapture()
	defer s.StopCapture()

	var buf strings.Builder
	deadline := time.After(timeout)

	for {
		select {
		case <-deadline:
			clean := normalizeTerminalText(buf.String())
			tail := clean
			if len(tail) > 200 {
				tail = tail[len(tail)-200:]
			}
			log.Printf("[wait-prompt] TIMEOUT for session %s, tail: %q", s.ID, tail)
			return false
		case chunk, ok := <-ch:
			if !ok {
				return false
			}
			buf.WriteString(chunk)
			clean := normalizeTerminalText(buf.String())
			if endsWithPrompt(clean) {
				log.Printf("[wait-prompt] prompt detected for session %s", s.ID)
				return true
			}
		}
	}
}

// CaptureOutput sends a command to a session and captures the output until
// either a prompt is detected, idle timeout expires, or max capture time is reached.
func CaptureOutput(ctx context.Context, s *Session, command string, idleTimeout time.Duration) (string, error) {
	ch := s.StartCapture()
	defer s.StopCapture()

	// Split write: text first, then Enter separately.
	text := strings.TrimRight(command, "\r\n")
	if _, err := s.PTY.Write([]byte(text)); err != nil {
		return "", err
	}
	time.Sleep(150 * time.Millisecond)
	if _, err := s.PTY.Write(EnterKey(s.AgentType)); err != nil {
		return "", err
	}

	var buf strings.Builder
	idle := time.NewTimer(idleTimeout)
	defer idle.Stop()

	// Hard upper limit to prevent infinite capture on streaming output.
	hardLimit := time.NewTimer(maxCaptureTime)
	defer hardLimit.Stop()

	commandSent := strings.TrimRight(command, "\r\n")
	echoSkipped := false
	sawResponse := false

	for {
		select {
		case <-ctx.Done():
			return finalizeOutput(s.AgentType, buf.String(), commandSent), nil

		case <-hardLimit.C:
			return finalizeOutput(s.AgentType, buf.String(), commandSent), nil

		case chunk, ok := <-ch:
			if !ok {
				return finalizeOutput(s.AgentType, buf.String(), commandSent), nil
			}
			buf.WriteString(chunk)
			idle.Reset(idleTimeout)

			if !echoSkipped {
				clean := normalizeTerminalText(buf.String())
				if strings.Contains(clean, commandSent) {
					echoSkipped = true
				}
			}

			clean := normalizeTerminalText(buf.String())
			if providerReply(s.AgentType, filterAgentNoise(s.AgentType, stripTUIArtifacts(clean))) != "" {
				sawResponse = true
			}
			if echoSkipped && providerAtPrompt(s.AgentType, clean) && sawResponse {
				return finalizeOutput(s.AgentType, buf.String(), commandSent), nil
			}

		case <-idle.C:
			return finalizeOutput(s.AgentType, buf.String(), commandSent), nil
		}
	}
}

// finalizeOutput cleans raw captured output: strips echo, TUI noise, prompt, and truncates.
func finalizeOutput(agentType AgentType, raw, command string) string {
	clean := filterAgentNoise(agentType, stripTUIArtifacts(raw))

	// Remove the echoed command line.
	if idx := strings.Index(clean, command); idx >= 0 {
		after := idx + len(command)
		for after < len(clean) && (clean[after] == '\r' || clean[after] == '\n') {
			after++
		}
		clean = clean[after:]
	}

	// Remove trailing prompt.
	for _, re := range promptPatterns {
		loc := re.FindAllStringIndex(clean, -1)
		if len(loc) > 0 {
			last := loc[len(loc)-1]
			if last[1] >= len(strings.TrimRight(clean, " \t\r\n")) {
				clean = clean[:last[0]]
			}
		}
	}

	clean = filterAgentNoise(agentType, clean)
	clean = strings.TrimSpace(clean)

	if reply := providerReply(agentType, clean); reply != "" {
		return reply
	}

	// Truncate if too large.
	if len(clean) > maxOutputChars {
		clean = clean[:maxOutputChars] + "\n\n... [truncated, " + fmt.Sprintf("%d", len(clean)) + " chars total]"
	}

	return clean
}
