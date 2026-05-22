package tools

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/blamely/blamely/internal/daemon"
)

// mockSink records every Event written to it so tests can inspect them.
type mockSink struct {
	events []daemon.Event
}

func (m *mockSink) Record(ev daemon.Event) error {
	m.events = append(m.events, ev)
	return nil
}

// ---- parsePatchBody tests ----

func TestParsePatchBody_SingleFile(t *testing.T) {
	body := "*** Begin Patch\n" +
		"*** Update File: /repo/main.go\n" +
		"@@\n" +
		"-old line\n" +
		"+new line one\n" +
		"+new line two\n" +
		" unchanged\n" +
		"*** End Patch\n"
	files := parsePatchBody(body)
	if len(files) != 1 {
		t.Fatalf("want 1 file, got %d", len(files))
	}
	f := files[0]
	if f.Path != "/repo/main.go" {
		t.Errorf("path: want /repo/main.go, got %q", f.Path)
	}
	if f.EndLine != 2 {
		t.Errorf("end_line (added line count): want 2, got %d", f.EndLine)
	}
	if f.StartLine != 1 {
		t.Errorf("start_line: want 1, got %d", f.StartLine)
	}
	if f.ContentSHA == "" {
		t.Error("content SHA should not be empty")
	}
}

func TestParsePatchBody_MultipleFiles(t *testing.T) {
	body := "*** Begin Patch\n" +
		"*** Update File: /repo/a.go\n" +
		"@@\n" +
		"+func A() {}\n" +
		"*** Update File: /repo/b.go\n" +
		"@@\n" +
		"+func B() {}\n" +
		"+func C() {}\n" +
		"*** End Patch\n"
	files := parsePatchBody(body)
	if len(files) != 2 {
		t.Fatalf("want 2 files, got %d", len(files))
	}
	if files[0].Path != "/repo/a.go" {
		t.Errorf("first file: want /repo/a.go, got %q", files[0].Path)
	}
	if files[0].EndLine != 1 {
		t.Errorf("a.go: want 1 added line, got %d", files[0].EndLine)
	}
	if files[1].Path != "/repo/b.go" {
		t.Errorf("second file: want /repo/b.go, got %q", files[1].Path)
	}
	if files[1].EndLine != 2 {
		t.Errorf("b.go: want 2 added lines, got %d", files[1].EndLine)
	}
}

func TestParsePatchBody_AddFile(t *testing.T) {
	body := "*** Begin Patch\n" +
		"*** Add File: /repo/new.go\n" +
		"+package x\n" +
		"+const N = 1\n" +
		"*** End Patch\n"
	files := parsePatchBody(body)
	if len(files) != 1 {
		t.Fatalf("want 1 file, got %d", len(files))
	}
	if files[0].Path != "/repo/new.go" {
		t.Errorf("path: want /repo/new.go, got %q", files[0].Path)
	}
}

func TestParsePatchBody_Empty(t *testing.T) {
	files := parsePatchBody("")
	if len(files) != 0 {
		t.Errorf("want 0 files, got %d", len(files))
	}
}

func TestParsePatchBody_DeleteFile(t *testing.T) {
	body := "*** Begin Patch\n*** Delete File: /repo/old.go\n*** End Patch\n"
	files := parsePatchBody(body)
	if len(files) != 0 {
		t.Errorf("delete should produce 0 added lines, got %d files", len(files))
	}
}

// ---- looksLikePatch tests ----

func TestLooksLikePatch(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"apply_patch", true},
		{"APPLY_PATCH", true},
		{"patch_file", true},
		{"shell", true},
		{"read_file", false},
		{"search", false},
		{"", false},
	}
	for _, c := range cases {
		if got := looksLikePatch(c.name); got != c.want {
			t.Errorf("looksLikePatch(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

// ---- processCodexLine / buffer-flush tests ----

func TestProcessCodexLine_BufferAndFlushOnUsage(t *testing.T) {
	sink := &mockSink{}
	st := &codexState{sink: sink}

	// Line 1: response.start — just sets model
	line1 := `{"type":"response.start","model":"gpt-5-codex","timestamp":"2026-01-01T00:00:00Z"}`
	processCodexLine([]byte(line1), st)
	if len(sink.events) != 0 {
		t.Errorf("response.start should not flush: got %d events", len(sink.events))
	}

	// Line 2: function_call apply_patch — should buffer (not yet recorded)
	patch := "*** Begin Patch\n*** Update File: /repo/x.go\n@@\n+func X() {}\n*** End Patch\n"
	line2, _ := json.Marshal(map[string]any{
		"type":      "function_call",
		"name":      "apply_patch",
		"arguments": json.RawMessage(`"` + escapeForJSON(patch) + `"`),
	})
	processCodexLine(line2, st)
	if len(sink.events) != 0 {
		t.Errorf("apply_patch should be buffered before usage: got %d events", len(sink.events))
	}

	// Line 3: response.complete with usage — should flush with tokens
	line3 := `{"type":"response.complete","usage":{"input_tokens":3200,"output_tokens":180,"cache_read_input_tokens":1000,"cache_creation_input_tokens":50}}`
	processCodexLine([]byte(line3), st)
	if len(sink.events) != 1 {
		t.Fatalf("expected 1 event after flush, got %d", len(sink.events))
	}
	ev := sink.events[0]
	if ev.Tool != "codex" {
		t.Errorf("tool: want codex, got %s", ev.Tool)
	}
	if ev.Model != "gpt-5-codex" {
		t.Errorf("model: want gpt-5-codex, got %s", ev.Model)
	}
	if ev.InputTokens == nil || *ev.InputTokens != 3200 {
		t.Errorf("input_tokens: want 3200, got %v", ev.InputTokens)
	}
	if ev.OutputTokens == nil || *ev.OutputTokens != 180 {
		t.Errorf("output_tokens: want 180, got %v", ev.OutputTokens)
	}
	if ev.CacheReadTokens == nil || *ev.CacheReadTokens != 1000 {
		t.Errorf("cache_read: want 1000, got %v", ev.CacheReadTokens)
	}
	if ev.CacheWriteTokens == nil || *ev.CacheWriteTokens != 50 {
		t.Errorf("cache_write: want 50, got %v", ev.CacheWriteTokens)
	}
}

func TestProcessCodexLine_FlushWithoutTokensOnShutdown(t *testing.T) {
	sink := &mockSink{}
	st := &codexState{sink: sink, model: "gpt-4"}

	patch := "*** Begin Patch\n*** Update File: /repo/y.go\n@@\n+func Y() {}\n*** End Patch\n"
	line, _ := json.Marshal(map[string]any{
		"type":      "function_call",
		"name":      "apply_patch",
		"arguments": json.RawMessage(`"` + escapeForJSON(patch) + `"`),
	})
	processCodexLine(line, st)
	if len(sink.events) != 0 {
		t.Fatalf("should still be buffered, got %d events", len(sink.events))
	}
	// Simulate daemon shutdown — flush with hasTokens=false
	st.flush(0, 0, 0, 0, false)
	if len(sink.events) != 1 {
		t.Fatalf("expected 1 event after shutdown flush, got %d", len(sink.events))
	}
	if sink.events[0].InputTokens != nil {
		t.Errorf("expected nil input_tokens on no-token flush")
	}
}

func TestProcessCodexLine_ModelCarriedForward(t *testing.T) {
	sink := &mockSink{}
	st := &codexState{sink: sink}

	// Set model via response.start
	processCodexLine([]byte(`{"type":"response.start","model":"gpt-codex-v2"}`), st)

	// Two patches in one turn — both should carry the same model
	patch1 := "*** Begin Patch\n*** Update File: /repo/p1.go\n@@\n+func P1() {}\n*** End Patch\n"
	patch2 := "*** Begin Patch\n*** Update File: /repo/p2.go\n@@\n+func P2() {}\n*** End Patch\n"

	processCodexLine(mustMarshalFuncCall("apply_patch", patch1), st)
	processCodexLine(mustMarshalFuncCall("apply_patch", patch2), st)

	// Flush
	st.flush(100, 10, 0, 0, true)
	if len(sink.events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(sink.events))
	}
	for _, ev := range sink.events {
		if ev.Model != "gpt-codex-v2" {
			t.Errorf("model not carried forward: got %s", ev.Model)
		}
	}
}

func TestProcessCodexLine_MessageUsageShape(t *testing.T) {
	// Some Codex versions put usage under .message.usage
	sink := &mockSink{}
	st := &codexState{sink: sink, model: "codex-m"}

	patch := "*** Begin Patch\n*** Update File: /repo/z.go\n@@\n+var Z = 1\n*** End Patch\n"
	processCodexLine(mustMarshalFuncCall("apply_patch", patch), st)

	line := `{"type":"assistant","message":{"model":"codex-m","usage":{"input_tokens":50,"output_tokens":5,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`
	processCodexLine([]byte(line), st)

	if len(sink.events) != 1 {
		t.Fatalf("expected flush after message.usage, got %d events", len(sink.events))
	}
	if *sink.events[0].InputTokens != 50 {
		t.Errorf("input_tokens: want 50, got %d", *sink.events[0].InputTokens)
	}
}

// ---- parseCodexTimestamp ----

func TestParseCodexTimestamp_RFC3339(t *testing.T) {
	ts := parseCodexTimestamp("2026-05-21T12:00:00Z")
	expected, _ := time.Parse(time.RFC3339, "2026-05-21T12:00:00Z")
	if !ts.Equal(expected) {
		t.Errorf("want %v, got %v", expected, ts)
	}
}

func TestParseCodexTimestamp_Empty(t *testing.T) {
	ts := parseCodexTimestamp("")
	if !ts.IsZero() {
		t.Errorf("empty timestamp should be zero, got %v", ts)
	}
}

func TestParseCodexTimestamp_Garbage(t *testing.T) {
	ts := parseCodexTimestamp("not a timestamp")
	if !ts.IsZero() {
		t.Errorf("garbage should be zero, got %v", ts)
	}
}

// ---- helpers ----

func escapeForJSON(s string) string {
	b, _ := json.Marshal(s)
	// b includes surrounding quotes — strip them
	return string(b[1 : len(b)-1])
}

func mustMarshalFuncCall(name, patch string) []byte {
	patchJSON, _ := json.Marshal(map[string]any{"input": patch})
	b, _ := json.Marshal(map[string]any{
		"type":      "function_call",
		"name":      name,
		"arguments": json.RawMessage(patchJSON),
	})
	return b
}
