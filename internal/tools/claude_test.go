package tools

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// buildPayload creates a temp git repo, writes a file into it, and returns
// the JSON hook payload that Blamely expects from a PostToolUse hook.
// Using a real git repo ensures gitutil.RepoID resolves correctly if the
// daemon happens to be running during the test.
func buildPayload(t *testing.T, extra map[string]any) string {
	t.Helper()
	dir := t.TempDir()

	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@l"},
		{"config", "user.name", "T"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Skipf("git not available: %v", err)
		}
	}

	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("line1\nline2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "-A"},
		{"commit", "-q", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		_ = cmd.Run()
	}

	m := map[string]any{
		"session_id":  "sess-123",
		"cwd":         dir,
		"tool_name":   "Write",
		"tool_input":  map[string]any{"file_path": p, "content": "line1\nline2\n"},
		"tool_output": `{"success":true}`,
	}
	for k, v := range extra {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// TestRecordClaudeFromStdin_ClaudePayload verifies that a payload without
// cursor_version is handled without error (records as claude).
func TestRecordClaudeFromStdin_ClaudePayload_NoError(t *testing.T) {
	payload := buildPayload(t, nil)
	err := RecordClaudeFromStdin(bytes.NewReader([]byte(payload)))
	if err != nil {
		t.Errorf("claude payload: unexpected error: %v", err)
	}
}

// TestRecordClaudeFromStdin_CursorPayload verifies that a payload with
// cursor_version is handled without error (records as cursor).
func TestRecordClaudeFromStdin_CursorPayload_NoError(t *testing.T) {
	payload := buildPayload(t, map[string]any{
		"cursor_version": "3.4.20",
		"model":          "composer-2.5",
	})
	err := RecordClaudeFromStdin(bytes.NewReader([]byte(payload)))
	if err != nil {
		t.Errorf("cursor payload: unexpected error: %v", err)
	}
}

// TestRecordClaudeFromStdin_CursorAliasConversationID verifies that Cursor's
// conversation_id field is treated as session_id.
func TestRecordClaudeFromStdin_CursorAliasConversationID(t *testing.T) {
	payload := buildPayload(t, map[string]any{
		"conversation_id": "conv-xyz",
		"cursor_version":  "3.4.20",
		"model":           "composer-2.5",
	})
	err := RecordClaudeFromStdin(bytes.NewReader([]byte(payload)))
	if err != nil {
		t.Errorf("cursor conversation_id alias: unexpected error: %v", err)
	}
}

// TestRecordClaudeFromStdin_BashTool verifies that a non-file tool (Bash)
// is a no-op — it doesn't target a file so nothing is recorded.
func TestRecordClaudeFromStdin_BashTool_NoOp(t *testing.T) {
	m := map[string]any{
		"session_id": "s",
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": "ls"},
	}
	b, _ := json.Marshal(m)
	if err := RecordClaudeFromStdin(bytes.NewReader(b)); err != nil {
		t.Errorf("Bash tool should be no-op, got: %v", err)
	}
}

// TestRecentlyChangedFiles verifies the git-status-based detection that backs
// Bash file-write attribution: an untracked source file just written shows up,
// while ignored paths and unchanged files do not.
func TestRecentlyChangedFiles(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@l"},
		{"config", "user.name", "T"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Skipf("git not available: %v", err)
		}
	}
	// gitignored file must be excluded.
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignored.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A freshly written, untracked source file — the Bash-create case.
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ignored.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := recentlyChangedFiles(dir, bashWriteWindow)
	if len(got) != 1 || got[0] != "new.txt" {
		t.Fatalf("expected [new.txt], got %v", got)
	}

	// A file last modified outside the window must not be reported.
	old := filepath.Join(dir, "old.txt")
	if err := os.WriteFile(old, []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-bashWriteWindow - time.Minute)
	if err := os.Chtimes(old, stale, stale); err != nil {
		t.Fatal(err)
	}
	for _, f := range recentlyChangedFiles(dir, bashWriteWindow) {
		if f == "old.txt" {
			t.Error("stale file should be excluded by the mtime window")
		}
	}
}

// TestRecordClaudeFromStdin_MalformedJSON expects a parse error.
func TestRecordClaudeFromStdin_MalformedJSON(t *testing.T) {
	err := RecordClaudeFromStdin(bytes.NewReader([]byte("not json")))
	if err == nil {
		t.Error("expected error for malformed JSON")
	}
}

// TestRecordClaudeFromStdin_EmptyObject is a no-op: no tool_name → no file targeted.
func TestRecordClaudeFromStdin_EmptyObject(t *testing.T) {
	if err := RecordClaudeFromStdin(bytes.NewReader([]byte("{}"))); err != nil {
		t.Errorf("empty object should be no-op, got: %v", err)
	}
}

// TestExtractClaudeRanges_EditDeletion verifies that an Edit with an empty
// new_string and a non-empty old_string is treated as a pure deletion:
// no post-image line range (the text is gone), but `suggested_lines` is
// credited so the AI gets attribution for the lines it removed.
func TestExtractClaudeRanges_EditDeletion(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"file_path":  "/tmp/x.go",
		"old_string": "a\nb\nc\nd",
		"new_string": "",
	})
	p := claudeHookPayload{ToolName: "Edit", ToolInput: raw}
	file, ranges, suggested, err := extractClaudeRanges(p)
	if err != nil {
		t.Fatalf("extractClaudeRanges: %v", err)
	}
	if file != "/tmp/x.go" {
		t.Errorf("file: want /tmp/x.go, got %q", file)
	}
	if ranges != nil {
		t.Errorf("ranges should be nil for pure deletion, got %+v", ranges)
	}
	if suggested != 4 {
		t.Errorf("suggested: want 4 (deleted lines), got %d", suggested)
	}
}

// TestExtractClaudeRanges_MultiEditMixedDeletion: one sub-edit adds 2 lines,
// another deletes 4. Suggested should sum both.
func TestExtractClaudeRanges_MultiEditMixedDeletion(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"file_path": "/tmp/m.go",
		"edits": []map[string]any{
			{"old_string": "x", "new_string": "x1\nx2"},                  // +2
			{"old_string": "del1\ndel2\ndel3\ndel4", "new_string": ""},   // -4
		},
	})
	p := claudeHookPayload{ToolName: "MultiEdit", ToolInput: raw}
	_, _, suggested, err := extractClaudeRanges(p)
	if err != nil {
		t.Fatalf("extractClaudeRanges: %v", err)
	}
	if suggested != 6 {
		t.Errorf("suggested: want 6 (2 added + 4 deleted), got %d", suggested)
	}
}

// TestExtractClaudeRanges_EditWhitespaceOnly_TreatedAsDeletion: new_string
// that's just whitespace (e.g. "   " or "\n\n") is also treated as a
// deletion when old_string was non-empty.
func TestExtractClaudeRanges_EditWhitespaceOnlyTreatedAsDeletion(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"file_path":  "/tmp/w.go",
		"old_string": "foo\nbar\nbaz",
		"new_string": "  \n  \n", // whitespace-only
	})
	p := claudeHookPayload{ToolName: "Edit", ToolInput: raw}
	_, ranges, suggested, _ := extractClaudeRanges(p)
	if ranges != nil {
		t.Errorf("whitespace-only new_string should produce no ranges, got %+v", ranges)
	}
	if suggested != 3 {
		t.Errorf("suggested should reflect deleted lines (3), got %d", suggested)
	}
}
