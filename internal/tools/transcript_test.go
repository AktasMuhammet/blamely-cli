package tools

import (
	"os"
	"strings"
	"testing"
	"time"
)

// mustNanos parses an RFC3339 timestamp to Unix nanoseconds, failing the test
// on a bad input.
func mustNanos(t *testing.T, rfc3339 string) int64 {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		t.Fatalf("parse %q: %v", rfc3339, err)
	}
	return tm.UnixNano()
}

func TestScanTranscriptUsage_Empty(t *testing.T) {
	u, err := scanTranscriptUsage(strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if u != nil {
		t.Fatalf("expected nil for empty transcript, got %+v", u)
	}
}

func TestScanTranscriptUsage_NoAssistantMessages(t *testing.T) {
	input := `{"type":"user","message":{"role":"user","content":"hello"}}` + "\n" +
		`{"type":"tool_result","content":"result"}` + "\n"
	u, err := scanTranscriptUsage(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if u != nil {
		t.Fatalf("expected nil when no assistant messages, got %+v", u)
	}
}

func TestScanTranscriptUsage_AssistantWithoutUsage(t *testing.T) {
	input := `{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-7"}}` + "\n"
	u, err := scanTranscriptUsage(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if u != nil {
		t.Fatalf("expected nil when usage is zero/absent, got %+v", u)
	}
}

func TestScanTranscriptUsage_FullUsage(t *testing.T) {
	input := `{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":1200,"output_tokens":340,"cache_read_input_tokens":800,"cache_creation_input_tokens":150}}}` + "\n"
	u, err := scanTranscriptUsage(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if u == nil {
		t.Fatal("expected non-nil usage")
	}
	if u.Model != "claude-opus-4-7" {
		t.Errorf("model: want claude-opus-4-7, got %s", u.Model)
	}
	if u.InputTokens != 1200 {
		t.Errorf("input_tokens: want 1200, got %d", u.InputTokens)
	}
	if u.OutputTokens != 340 {
		t.Errorf("output_tokens: want 340, got %d", u.OutputTokens)
	}
	if u.CacheReadTokens != 800 {
		t.Errorf("cache_read: want 800, got %d", u.CacheReadTokens)
	}
	if u.CacheWriteTokens != 150 {
		t.Errorf("cache_write: want 150, got %d", u.CacheWriteTokens)
	}
}

func TestScanTranscriptUsage_LastAssistantTurnWins(t *testing.T) {
	// Two assistant turns — the test should return the LAST one with usage.
	input := `{"type":"assistant","message":{"role":"assistant","model":"claude-sonnet-4-6","usage":{"input_tokens":100,"output_tokens":10}}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":500,"output_tokens":50}}}` + "\n"
	u, err := scanTranscriptUsage(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if u == nil {
		t.Fatal("expected non-nil")
	}
	if u.Model != "claude-opus-4-7" {
		t.Errorf("want last model claude-opus-4-7, got %s", u.Model)
	}
	if u.InputTokens != 500 {
		t.Errorf("want 500 input_tokens from last turn, got %d", u.InputTokens)
	}
}

func TestScanTranscriptUsage_MalformedLinesSkipped(t *testing.T) {
	input := "not json at all\n" +
		`{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":42,"output_tokens":7}}}` + "\n"
	u, err := scanTranscriptUsage(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if u == nil || u.InputTokens != 42 {
		t.Fatalf("expected usage from valid line, got %+v", u)
	}
}

// scanConversation tests

func TestScanConversation_PlainStringUserTurn(t *testing.T) {
	// User turns where content is a plain JSON string (not an array).
	input := `{"type":"user","message":{"role":"user","content":"Fix the auth bug"}}` + "\n" +
		`{"type":"assistant","message":{"model":"claude-opus-4-7","content":[{"type":"text","text":"I'll fix that now."}],"usage":{"input_tokens":10,"output_tokens":5}}}` + "\n"
	turns, err := scanConversation(strings.NewReader(input), 10, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 {
		t.Fatalf("want 2 turns, got %d: %+v", len(turns), turns)
	}
	if turns[0].Role != "user" || turns[0].Text != "Fix the auth bug" {
		t.Errorf("user turn: got role=%q text=%q", turns[0].Role, turns[0].Text)
	}
	if turns[1].Role != "assistant" || turns[1].Text != "I'll fix that now." {
		t.Errorf("assistant turn: got role=%q text=%q", turns[1].Role, turns[1].Text)
	}
}

func TestScanConversation_TimeWindowScopesToCommitCycle(t *testing.T) {
	// Three user turns across two commits. The window (since, until] should keep
	// only the turn made between the previous commit and this one.
	input := `{"type":"user","timestamp":"2026-01-01T10:00:00Z","message":{"content":"create a simple html page"}}` + "\n" +
		`{"type":"user","timestamp":"2026-01-01T11:00:00Z","message":{"content":"add a simple button"}}` + "\n" +
		`{"type":"user","timestamp":"2026-01-01T12:00:00Z","message":{"content":"change the color"}}` + "\n"

	prevCommit := mustNanos(t, "2026-01-01T10:30:00Z") // first commit was at 10:30
	thisCommit := mustNanos(t, "2026-01-01T11:30:00Z") // second commit at 11:30

	turns, err := scanConversation(strings.NewReader(input), 10, 0, prevCommit, thisCommit)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 {
		t.Fatalf("want 1 turn in window, got %d: %+v", len(turns), turns)
	}
	if turns[0].Text != "add a simple button" {
		t.Errorf("want only 'add a simple button', got %q", turns[0].Text)
	}
}

func TestScanConversation_NoTimestampFailsOpen(t *testing.T) {
	// A turn without a timestamp is kept even when a window is set, so formats
	// lacking timestamps aren't silently dropped.
	input := `{"type":"user","message":{"content":"no timestamp here"}}` + "\n"
	turns, err := scanConversation(strings.NewReader(input), 10, 0, mustNanos(t, "2026-01-01T10:00:00Z"), mustNanos(t, "2026-01-01T12:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0].Text != "no timestamp here" {
		t.Fatalf("want the un-timestamped turn kept, got %+v", turns)
	}
}

func TestScanConversation_HumanTurnContentType(t *testing.T) {
	// User turns where content is an array with type "human_turn".
	input := `{"type":"user","message":{"content":[{"type":"human_turn","text":"Refactor this function"}]}}` + "\n" +
		`{"type":"assistant","message":{"model":"claude-opus-4-7","content":[{"type":"text","text":"Sure, here is the refactor."}],"usage":{"input_tokens":10,"output_tokens":5}}}` + "\n"
	turns, err := scanConversation(strings.NewReader(input), 10, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 {
		t.Fatalf("want 2 turns, got %d: %+v", len(turns), turns)
	}
	if turns[0].Role != "user" || turns[0].Text != "Refactor this function" {
		t.Errorf("user turn: got role=%q text=%q", turns[0].Role, turns[0].Text)
	}
}

func TestScanConversation_TextArrayContentType(t *testing.T) {
	// Both turns using standard array with type "text".
	input := `{"type":"user","message":{"content":[{"type":"text","text":"Add unit tests"}]}}` + "\n" +
		`{"type":"assistant","message":{"model":"claude-opus-4-7","content":[{"type":"text","text":"I'll add tests for each case."},{"type":"tool_use","id":"t1","name":"Edit","input":{}}],"usage":{"input_tokens":10,"output_tokens":5}}}` + "\n"
	turns, err := scanConversation(strings.NewReader(input), 10, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 {
		t.Fatalf("want 2 turns, got %d", len(turns))
	}
	// tool_use block must not bleed into the assistant text.
	if turns[1].Text != "I'll add tests for each case." {
		t.Errorf("assistant text: got %q", turns[1].Text)
	}
}

func TestScanConversation_SkipsToolUseOnlyTurns(t *testing.T) {
	// An assistant turn with only a tool_use block (no text) should be skipped.
	input := `{"type":"assistant","message":{"model":"claude-opus-4-7","content":[{"type":"tool_use","id":"t1","name":"Edit","input":{}}],"usage":{"input_tokens":5,"output_tokens":2}}}` + "\n" +
		`{"type":"assistant","message":{"model":"claude-opus-4-7","content":[{"type":"text","text":"Done."}],"usage":{"input_tokens":5,"output_tokens":2}}}` + "\n"
	turns, err := scanConversation(strings.NewReader(input), 10, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0].Text != "Done." {
		t.Fatalf("want 1 turn with text 'Done.', got %+v", turns)
	}
}

func TestReadTranscriptGenType_CLI(t *testing.T) {
	f := writeTempJSONL(t,
		`{"type":"user","entrypoint":"cli","message":{"role":"user","content":"fix it"}}`,
		`{"type":"assistant","message":{"model":"claude-opus-4-7","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":10,"output_tokens":5}}}`,
	)
	if got := ReadTranscriptGenType(f); got != "cli" {
		t.Errorf("want cli, got %q", got)
	}
}

func TestReadTranscriptGenType_SDKTS(t *testing.T) {
	f := writeTempJSONL(t,
		`{"type":"user","entrypoint":"sdk-ts","message":{"role":"user","content":"run"}}`,
	)
	if got := ReadTranscriptGenType(f); got != "cli" {
		t.Errorf("want cli for sdk-ts, got %q", got)
	}
}

func TestReadTranscriptGenType_Interactive(t *testing.T) {
	f := writeTempJSONL(t,
		`{"type":"user","entrypoint":"claude-vscode","message":{"role":"user","content":"hello"}}`,
	)
	if got := ReadTranscriptGenType(f); got != "chat" {
		t.Errorf("want chat for claude-vscode, got %q", got)
	}
}

func TestReadTranscriptGenType_NoEntrypoint(t *testing.T) {
	// Cursor transcripts have no entrypoint field — should default to chat.
	f := writeTempJSONL(t,
		`{"role":"user","message":{"content":[{"type":"text","text":"hi"}]}}`,
	)
	if got := ReadTranscriptGenType(f); got != "chat" {
		t.Errorf("want chat when entrypoint absent, got %q", got)
	}
}

func TestReadTranscriptGenType_EmptyPath(t *testing.T) {
	if got := ReadTranscriptGenType(""); got != "chat" {
		t.Errorf("want chat for empty path, got %q", got)
	}
}

// writeTempJSONL writes lines to a temp file and returns its path.
func writeTempJSONL(t *testing.T, lines ...string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "transcript-*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		f.WriteString(l + "\n")
	}
	f.Close()
	return f.Name()
}

func TestScanConversation_MaxTurnsAndTruncation(t *testing.T) {
	input := `{"type":"user","message":{"content":[{"type":"text","text":"first"}]}}` + "\n" +
		`{"type":"user","message":{"content":[{"type":"text","text":"second"}]}}` + "\n" +
		`{"type":"user","message":{"content":[{"type":"text","text":"third"}]}}` + "\n"
	turns, err := scanConversation(strings.NewReader(input), 2, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 {
		t.Fatalf("want last 2 turns, got %d", len(turns))
	}
	if turns[0].Text != "second" || turns[1].Text != "third" {
		t.Errorf("want last 2 turns, got %+v", turns)
	}
}
