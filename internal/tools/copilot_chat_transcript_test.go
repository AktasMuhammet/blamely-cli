package tools

import (
	"os"
	"path/filepath"
	"testing"
)


func TestDisplayModel(t *testing.T) {
	cases := map[string]string{
		"copilot/gpt-5-mini":      "gpt-5-mini",
		"copilot/claude-opus-4.6": "claude-opus-4.6",
		"gpt-4o":                  "gpt-4o",
		"":                        "",
	}
	for id, want := range cases {
		if got := displayModel(id); got != want {
			t.Errorf("displayModel(%q) = %q, want %q", id, got, want)
		}
	}
}

// sampleChatJSONL is a minimal delta-encoded chat session: a snapshot that sets
// the selected model, two appended requests, streamed response chunks (one with
// a skipped "thinking" part), and per-request token usage via result deltas.
const sampleChatJSONL = `{"kind":0,"v":{"requests":[],"inputState":{"selectedModel":{"identifier":"copilot/gpt-5-mini"}}}}
{"kind":2,"k":["requests"],"v":[{"timestamp":1000,"message":{"text":"hello"},"response":[]}]}
{"kind":2,"k":["requests",0,"response"],"v":[{"kind":"thinking","value":"internal reasoning"},{"value":"Hi "},{"value":"there"}]}
{"kind":1,"k":["requests",0,"result"],"v":{"usage":{"promptTokens":100,"completionTokens":20}}}
{"kind":2,"k":["requests"],"v":[{"timestamp":2000,"message":{"text":"second"},"response":[{"value":"reply2"}]}]}
{"kind":1,"k":["requests",1,"result"],"v":{"usage":{"promptTokens":50,"completionTokens":10}}}
`

func writeSample(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(p, []byte(sampleChatJSONL), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadChatSessionConversation(t *testing.T) {
	p := writeSample(t)
	turns, err := ReadChatSessionConversation(p, 10, 300, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []ConvTurn{
		{Role: "user", Text: "hello"},
		{Role: "assistant", Text: "Hi there"}, // chunks concatenated, thinking skipped
		{Role: "user", Text: "second"},
		{Role: "assistant", Text: "reply2"},
	}
	if len(turns) != len(want) {
		t.Fatalf("turns: want %d, got %d (%+v)", len(want), len(turns), turns)
	}
	for i := range want {
		if turns[i] != want[i] {
			t.Errorf("turn %d: want %+v, got %+v", i, want[i], turns[i])
		}
	}
}

func TestReadChatSessionConversation_LastNTurns(t *testing.T) {
	p := writeSample(t)
	turns, err := ReadChatSessionConversation(p, 2, 300, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 {
		t.Fatalf("want 2 turns, got %d", len(turns))
	}
	// Should be the LAST two turns.
	if turns[0].Text != "second" || turns[1].Text != "reply2" {
		t.Errorf("want last two turns [second, reply2], got %+v", turns)
	}
}

// TestReadChatSessionConversation_WindowFilter is the core regression test for
// the bug where older requests in a long-running session bled into newer commits'
// notes. The sample has two requests at timestamps 1000ms and 2000ms; a window
// covering only 1500ms–3000ms should return only the second request.
func TestReadChatSessionConversation_WindowFilter(t *testing.T) {
	p := writeSample(t)
	// sinceNanos = 1500ms in ns, untilNanos = 3000ms in ns
	since := int64(1500 * 1e6)
	until := int64(3000 * 1e6)
	turns, err := ReadChatSessionConversation(p, 10, 300, since, until)
	if err != nil {
		t.Fatal(err)
	}
	// Only the second request (timestamp 2000ms) should appear.
	if len(turns) != 2 {
		t.Fatalf("want 2 turns (second request only), got %d: %+v", len(turns), turns)
	}
	if turns[0].Text != "second" || turns[1].Text != "reply2" {
		t.Errorf("want [second, reply2], got %+v", turns)
	}
}

func TestReadChatSessionUsage_SumAll(t *testing.T) {
	p := writeSample(t)
	u, err := ReadChatSessionUsage(p, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if u == nil {
		t.Fatal("expected usage, got nil")
	}
	if u.InputTokens != 150 || u.OutputTokens != 30 {
		t.Errorf("tokens: want input=150 output=30, got input=%d output=%d", u.InputTokens, u.OutputTokens)
	}
	if u.Model != "gpt-5-mini" {
		t.Errorf("model: want gpt-5-mini, got %q", u.Model)
	}
}

func TestReadChatSessionUsage_WindowFilter(t *testing.T) {
	p := writeSample(t)
	// Window covers only the first request (timestamp 1000ms → 1e9 ns).
	u, err := ReadChatSessionUsage(p, 0, int64(1500*1e6))
	if err != nil {
		t.Fatal(err)
	}
	if u == nil {
		t.Fatal("expected usage, got nil")
	}
	if u.InputTokens != 100 || u.OutputTokens != 20 {
		t.Errorf("windowed tokens: want input=100 output=20, got input=%d output=%d", u.InputTokens, u.OutputTokens)
	}
}

func TestReadChatSessionUsage_NoTokens(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "empty.jsonl")
	// Snapshot only, no requests with usage.
	if err := os.WriteFile(p, []byte(`{"kind":0,"v":{"requests":[],"inputState":{"selectedModel":{"identifier":"copilot/gpt-4o"}}}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := ReadChatSessionUsage(p, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if u != nil {
		t.Errorf("expected nil usage when no tokens present, got %+v", u)
	}
}
