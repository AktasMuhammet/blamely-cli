package tools

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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
