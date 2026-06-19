package tools

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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

func TestReadCodexSessionUsage_LatestBlock(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sess.jsonl")
	content := `{"type":"response.start","model":"gpt-5-codex"}
{"type":"response.complete","usage":{"input_tokens":100,"output_tokens":20}}
{"type":"response.complete","usage":{"input_tokens":500,"output_tokens":80,"cache_read_input_tokens":10,"cache_creation_input_tokens":5}}
`
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := ReadCodexSessionUsage(p)
	if err != nil {
		t.Fatal(err)
	}
	if u == nil {
		t.Fatal("expected usage")
	}
	if u.Model != "gpt-5-codex" {
		t.Errorf("model: want gpt-5-codex, got %q", u.Model)
	}
	if u.InputTokens != 500 || u.OutputTokens != 80 {
		t.Errorf("tokens: want 500/80, got %d/%d", u.InputTokens, u.OutputTokens)
	}
	if u.CacheReadTokens != 10 || u.CacheWriteTokens != 5 {
		t.Errorf("cache: want 10/5, got %d/%d", u.CacheReadTokens, u.CacheWriteTokens)
	}
}

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

// ---- wrapped session-line format (current Codex CLI / codex_vscode) ----
//
// Newer Codex CLI releases (observed at cli_version 0.137.x) wrap every
// session line as {"timestamp","type":"event_msg"|"turn_context"|...,
// "payload":{...}} instead of the old flat {"type","model"/"name", ...}
// shape `codexLine` was built for. processCodexLine must recognize both.

func gitInitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@l"}, {"config", "user.name", "T"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if err := cmd.Run(); err != nil {
			t.Skipf("git not available: %v", err)
		}
	}
	return repo
}

func mustMarshalWrapped(t *testing.T, typ string, payload map[string]any) []byte {
	t.Helper()
	p, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(map[string]any{
		"timestamp": "2026-06-08T00:00:00Z",
		"type":      typ,
		"payload":   json.RawMessage(p),
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestProcessCodexLine_WrappedFormat_AddFileWholeFile(t *testing.T) {
	repo := gitInitRepo(t)
	content := "line one\nline two\nline three\n"
	target := filepath.Join(repo, "new.go")
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	sink := &mockSink{}
	st := &codexState{sink: sink}

	processCodexLine(mustMarshalWrapped(t, "turn_context", map[string]any{"model": "gpt-5.5"}), st)

	patchEnd := mustMarshalWrapped(t, "event_msg", map[string]any{
		"type":    "patch_apply_end",
		"success": true,
		"changes": map[string]any{
			target: map[string]any{"type": "add", "content": content},
		},
	})
	processCodexLine(patchEnd, st)
	if len(sink.events) != 0 {
		t.Fatalf("patch_apply_end should buffer, not record immediately: got %d events", len(sink.events))
	}

	tokenCount := mustMarshalWrapped(t, "event_msg", map[string]any{
		"type": "token_count",
		"info": map[string]any{
			"last_token_usage": map[string]any{"input_tokens": 1200, "cached_input_tokens": 900, "output_tokens": 80},
		},
	})
	processCodexLine(tokenCount, st)

	if len(sink.events) != 1 {
		t.Fatalf("expected 1 event after token_count flush, got %d: %+v", len(sink.events), sink.events)
	}
	ev := sink.events[0]
	if ev.Tool != "codex" || ev.GenType != "cli" || ev.Confidence != "high" {
		t.Errorf("tool=%q gen_type=%q confidence=%q, want codex/cli/high", ev.Tool, ev.GenType, ev.Confidence)
	}
	if ev.Model != "gpt-5.5" {
		t.Errorf("model = %q, want gpt-5.5", ev.Model)
	}
	if ev.FilePath != "new.go" {
		t.Errorf("file_path = %q, want new.go", ev.FilePath)
	}
	// One single-line range per line, each with its own content SHA — not one
	// combined whole-file range — so per-line content_sha attribution can match.
	if len(ev.Lines) != 3 {
		t.Fatalf("lines = %+v, want 3 single-line ranges", ev.Lines)
	}
	for i := range ev.Lines {
		if ev.Lines[i].Start != i+1 || ev.Lines[i].End != i+1 {
			t.Errorf("lines[%d] = %+v, want %d..%d", i, ev.Lines[i], i+1, i+1)
		}
	}
	if ev.SuggestedLines != 3 {
		t.Errorf("suggested_lines = %d, want 3", ev.SuggestedLines)
	}
	if ev.InputTokens == nil || *ev.InputTokens != 1200 {
		t.Errorf("input_tokens = %v, want 1200", ev.InputTokens)
	}
	if ev.CacheReadTokens == nil || *ev.CacheReadTokens != 900 {
		t.Errorf("cache_read_tokens = %v, want 900", ev.CacheReadTokens)
	}
}

func TestProcessCodexLine_WrappedFormat_UpdateFileUnifiedDiff(t *testing.T) {
	repo := gitInitRepo(t)
	target := filepath.Join(repo, "index.html")
	finalContent := "<html>\n<head></head>\n<body>\n<!-- new line A -->\n<!-- new line B -->\n</body>\n</html>\n"
	if err := os.WriteFile(target, []byte(finalContent), 0o644); err != nil {
		t.Fatal(err)
	}

	sink := &mockSink{}
	st := &codexState{sink: sink, model: "gpt-5.5"}

	diff := "@@ -1,4 +1,6 @@\n <html>\n <head></head>\n <body>\n+<!-- new line A -->\n+<!-- new line B -->\n </body>\n </html>\n"
	patchEnd := mustMarshalWrapped(t, "event_msg", map[string]any{
		"type":    "patch_apply_end",
		"success": true,
		"changes": map[string]any{
			target: map[string]any{"type": "update", "unified_diff": diff},
		},
	})
	processCodexLine(patchEnd, st)
	st.flush(0, 0, 0, 0, false) // simulate shutdown drain (mirrors TestProcessCodexLine_FlushWithoutTokensOnShutdown)

	if len(sink.events) != 1 {
		t.Fatalf("expected 1 event, got %d: %+v", len(sink.events), sink.events)
	}
	ev := sink.events[0]
	if ev.FilePath != "index.html" {
		t.Errorf("file_path = %q, want index.html", ev.FilePath)
	}
	if len(ev.Lines) != 2 || ev.Lines[0].Start != 4 || ev.Lines[1].Start != 5 {
		t.Errorf("lines = %+v, want ranges anchored at 4 and 5", ev.Lines)
	}
	if ev.SuggestedLines != 2 {
		t.Errorf("suggested_lines = %d, want 2", ev.SuggestedLines)
	}
}

// Regression: a Codex apply_patch that ONLY deletes lines (a hunk with '-' lines
// and no '+' lines) must still be recorded with its removed-line hashes. The
// update branch used to bail out when there were no added ranges, dropping the
// whole edit — so the deletion was never recorded and fell back to Human at
// commit time (observed on commit b7c4c2dc).
func TestProcessCodexLine_WrappedFormat_UpdateFilePureDeletion(t *testing.T) {
	repo := gitInitRepo(t)
	target := filepath.Join(repo, "welcome.html")
	// Post-deletion content on disk (the <title> line is gone).
	finalContent := "<html>\n<head>\n</head>\n<body>\n</body>\n</html>\n"
	if err := os.WriteFile(target, []byte(finalContent), 0o644); err != nil {
		t.Fatal(err)
	}

	sink := &mockSink{}
	st := &codexState{sink: sink, model: "gpt-5.5"}

	// Pure-deletion hunk: one '-' line, zero '+' lines.
	diff := "@@ -1,3 +1,2 @@\n <html>\n-<title>Welcome</title>\n <head>\n"
	patchEnd := mustMarshalWrapped(t, "event_msg", map[string]any{
		"type":    "patch_apply_end",
		"success": true,
		"changes": map[string]any{
			target: map[string]any{"type": "update", "unified_diff": diff},
		},
	})
	processCodexLine(patchEnd, st)
	st.flush(0, 0, 0, 0, false) // simulate shutdown drain

	if len(sink.events) != 1 {
		t.Fatalf("pure deletion must record 1 event, got %d: %+v", len(sink.events), sink.events)
	}
	ev := sink.events[0]
	if ev.Tool != "codex" || ev.GenType != "cli" {
		t.Errorf("tool=%q gen_type=%q, want codex/cli", ev.Tool, ev.GenType)
	}
	if ev.FilePath != "welcome.html" {
		t.Errorf("file_path = %q, want welcome.html", ev.FilePath)
	}
	if len(ev.Lines) != 0 {
		t.Errorf("a pure deletion has no added lines, got %+v", ev.Lines)
	}
	if len(ev.RemovedLines) != 1 {
		t.Fatalf("removed_lines = %d, want 1 (the deleted <title> line)", len(ev.RemovedLines))
	}
}

func TestProcessCodexLine_WrappedFormat_FailedPatchIgnored(t *testing.T) {
	sink := &mockSink{}
	st := &codexState{sink: sink}
	patchEnd := mustMarshalWrapped(t, "event_msg", map[string]any{
		"type":    "patch_apply_end",
		"success": false,
		"changes": map[string]any{
			"/repo/x.go": map[string]any{"type": "update", "unified_diff": "@@ -1 +1 @@\n-old\n+new\n"},
		},
	})
	processCodexLine(patchEnd, st)
	if len(st.pending) != 0 {
		t.Errorf("failed patch_apply_end should not be buffered, got %d pending", len(st.pending))
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

// ---- findCodexSessionFiles tests ----

func TestFindCodexSessionFiles_RecursesDatePartitionedDirs(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "2026", "06", "08")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(nested, "rollout-2026-06-08T00-14-45-abc.jsonl")
	if err := os.WriteFile(want, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Non-jsonl files anywhere in the tree must be ignored.
	if err := os.WriteFile(filepath.Join(nested, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".DS_Store"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := findCodexSessionFiles(dir)
	if len(got) != 1 || got[0] != want {
		t.Fatalf("findCodexSessionFiles = %v, want [%s]", got, want)
	}
}

func TestFindCodexSessionFiles_MissingDir(t *testing.T) {
	if got := findCodexSessionFiles(filepath.Join(t.TempDir(), "does-not-exist")); len(got) != 0 {
		t.Fatalf("findCodexSessionFiles on missing dir = %v, want empty", got)
	}
}
