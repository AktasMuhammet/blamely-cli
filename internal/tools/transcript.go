package tools

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// TranscriptUsage extracts model + token usage from the last assistant turn
// of a Claude Code transcript JSONL.
type TranscriptUsage struct {
	Model            string
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
}

// ConvTurn is one user or assistant turn from a Claude Code transcript.
type ConvTurn struct {
	Role string // "user" or "assistant"
	Text string // first text content block, truncated
}

// transcriptEntry covers two transcript JSONL formats:
//
//   Claude Code:  {"type":"user"|"assistant", "message":{...}}
//   Cursor:       {"role":"user"|"assistant", "message":{...}}
//
// Both are emitted as JSONL; we merge role/type so callers see a unified view.
type transcriptEntry struct {
	// Claude Code uses "type"; Cursor uses "role" — we read both.
	Type string `json:"type"`
	Role string `json:"role"`

	// Entrypoint is set on user turns by the claude CLI/SDK. Known values:
	//   "cli"           – `claude -p "..."` (non-interactive command)
	//   "sdk-ts"        – TypeScript SDK (programmatic)
	//   "claude-vscode" – Claude Code in VSCode (interactive)
	// Absent on assistant turns and on Cursor transcripts.
	Entrypoint string `json:"entrypoint"`

	// Timestamp is the RFC3339 wall-clock time of the turn (Claude Code writes
	// e.g. "2026-05-14T11:26:43.313Z"). Used to scope a commit's conversation to
	// the turns made since the previous commit. Absent on some entry kinds.
	Timestamp string `json:"timestamp"`

	Message struct {
		Model string `json:"model"`
		Usage struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
		// Content may be a plain string (some user turns) or an array of typed
		// content blocks. RawMessage keeps the outer struct parseable regardless
		// of which shape arrives.
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// ReadTranscriptGenType returns the gen_type ("chat" or "cli") for a Claude
// transcript JSONL by inspecting the first user turn's "entrypoint" field.
// Entrypoints "cli" and "sdk-ts" map to "cli"; all others (including
// "claude-vscode") map to "chat". Returns "chat" when the file is missing or
// has no user turn with an entrypoint.
func ReadTranscriptGenType(path string) string {
	if path == "" {
		return "chat"
	}
	f, err := os.Open(path)
	if err != nil {
		return "chat"
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<22)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var e transcriptEntry
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		if e.role() != "user" || e.Entrypoint == "" {
			continue
		}
		switch e.Entrypoint {
		case "cli", "sdk-ts":
			return "cli"
		default:
			return "chat"
		}
	}
	return "chat"
}

// extractText returns the first non-empty human-readable text from a content
// field that is either a plain JSON string or an array of content blocks.
// For arrays, "text" and "human_turn" block types are accepted; tool_use and
// tool_result blocks are skipped so they don't pollute conversation snippets.
func extractText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Plain string: some Claude Code versions encode user messages this way.
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	// Array of typed content blocks.
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		for _, b := range blocks {
			if b.Type == "text" || b.Type == "human_turn" {
				if t := strings.TrimSpace(b.Text); t != "" {
					return t
				}
			}
		}
	}
	return ""
}

// role returns the normalised "user" or "assistant" string for either format.
func (e *transcriptEntry) role() string {
	if e.Type != "" {
		return e.Type
	}
	return e.Role
}

// ReadTranscriptConversation returns up to maxTurns user/assistant turns from
// the transcript JSONL, taking the last maxTurns turns. Each turn's text is
// capped at maxChars characters. Tool-use and system entries are skipped.
// Reads the whole transcript (no time window) — use
// ReadTranscriptConversationWindow to scope to a commit's work cycle.
func ReadTranscriptConversation(path string, maxTurns, maxChars int) ([]ConvTurn, error) {
	return ReadTranscriptConversationWindow(path, maxTurns, maxChars, 0, 0)
}

// ReadTranscriptConversationWindow is ReadTranscriptConversation scoped to a
// time window: only turns whose timestamp falls in (sinceNanos, untilNanos] are
// returned. Pass 0 for either bound to leave that side open. This is what keeps
// a commit's note conversation to the turns made SINCE the previous commit, so
// each commit shows only its own prompts rather than the whole session history.
func ReadTranscriptConversationWindow(path string, maxTurns, maxChars int, sinceNanos, untilNanos int64) ([]ConvTurn, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()
	return scanConversation(f, maxTurns, maxChars, sinceNanos, untilNanos)
}

func scanConversation(r io.Reader, maxTurns, maxChars int, sinceNanos, untilNanos int64) ([]ConvTurn, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<22)
	var turns []ConvTurn
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var e transcriptEntry
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		role := e.role()
		if role != "user" && role != "assistant" {
			continue
		}
		// Time-window scope: when a bound is set, drop turns outside it. A turn
		// whose timestamp can't be parsed (ts==0) is KEPT (fail open) so formats
		// without a timestamp aren't silently lost — Claude turns always carry one.
		if sinceNanos > 0 || untilNanos > 0 {
			if ts := e.timestampNanos(); ts > 0 {
				if ts <= sinceNanos || (untilNanos > 0 && ts > untilNanos) {
					continue
				}
			}
		}
		text := extractText(e.Message.Content)
		if text == "" {
			continue
		}
		if maxChars > 0 && len(text) > maxChars {
			text = text[:maxChars] + "…"
		}
		turns = append(turns, ConvTurn{Role: role, Text: strings.TrimSpace(text)})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan transcript: %w", err)
	}
	// Return only the last maxTurns turns.
	if maxTurns > 0 && len(turns) > maxTurns {
		turns = turns[len(turns)-maxTurns:]
	}
	return turns, nil
}

// timestampNanos parses the entry's RFC3339 timestamp to Unix nanoseconds.
// Returns 0 when absent or unparseable.
func (e *transcriptEntry) timestampNanos() int64 {
	if e.Timestamp == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, e.Timestamp)
	if err != nil {
		return 0
	}
	return t.UnixNano()
}

func ReadTranscriptUsage(path string) (*TranscriptUsage, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()
	return scanTranscriptUsage(f)
}

func scanTranscriptUsage(r io.Reader) (*TranscriptUsage, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<22)
	var last *TranscriptUsage
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var e transcriptEntry
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		if e.role() != "assistant" || e.Message.Model == "" {
			continue
		}
		if e.Message.Usage.InputTokens == 0 && e.Message.Usage.OutputTokens == 0 {
			continue
		}
		last = &TranscriptUsage{
			Model:            e.Message.Model,
			InputTokens:      e.Message.Usage.InputTokens,
			OutputTokens:     e.Message.Usage.OutputTokens,
			CacheReadTokens:  e.Message.Usage.CacheReadInputTokens,
			CacheWriteTokens: e.Message.Usage.CacheCreationInputTokens,
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan transcript: %w", err)
	}
	return last, nil
}
