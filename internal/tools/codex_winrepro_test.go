package tools

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestCodexWinReproAddPatch feeds the EXACT event shape from the user's Windows
// rollout (session_meta originator=codex_vscode + event_msg/patch_apply_end with
// a type:"add" change) through the real parse+emit path, with the change key
// remapped to a temp git repo so it resolves on this host. Proves whether the
// parser handles the payload at all.
func TestCodexWinReproAddPatch(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) {
		c := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@t.t")
	run("config", "user.name", "t")

	content := "<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n  <title>Simple Contact</title>\n</head>\n<body>\n  <h1>Contact Us</h1>\n</body>\n</html>\n"
	target := filepath.Join(repo, "simple-contact.html")
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	sink := &mockSink{}
	st := &codexState{sink: sink}

	// 1. session_meta -> gen_type chat (VS Code surface)
	meta := `{"timestamp":"2026-06-24T10:09:59.796Z","type":"session_meta","payload":{"originator":"codex_vscode","source":"vscode"}}`
	processCodexLine([]byte(meta), st)
	if st.genType != "chat" {
		t.Fatalf("session_meta should set gen_type=chat, got %q", st.genType)
	}

	// 2. event_msg/patch_apply_end with a type:"add" change (the real shape)
	patchEnd := map[string]any{
		"timestamp": "2026-06-24T10:10:48.103Z",
		"type":      "event_msg",
		"payload": map[string]any{
			"type":    "patch_apply_end",
			"success": true,
			"changes": map[string]any{
				target: map[string]any{"type": "add", "content": content},
			},
		},
	}
	raw, _ := json.Marshal(patchEnd)
	processCodexLine(raw, st)

	// patch_apply_end buffers; nothing recorded until a token_count flush.
	if len(sink.events) != 0 {
		t.Fatalf("patch_apply_end should buffer, got %d recorded", len(sink.events))
	}
	if len(st.pending) != 1 {
		t.Fatalf("expected 1 buffered event, got %d", len(st.pending))
	}

	// 3. flush (token_count)
	tc := `{"timestamp":"2026-06-24T10:10:48.200Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"output_tokens":50,"cached_input_tokens":10}}}}`
	processCodexLine([]byte(tc), st)

	if len(sink.events) != 1 {
		t.Fatalf("expected 1 recorded event after flush, got %d", len(sink.events))
	}
	ev := sink.events[0]
	t.Logf("EVENT: tool=%s gen=%s repo=%q file=%q lines=%d", ev.Tool, ev.GenType, ev.RepoPath, ev.FilePath, len(ev.Lines))
	if ev.RepoPath == "" {
		t.Errorf("RepoPath empty -> event would be DROPPED by sink")
	}
	if ev.FilePath != "simple-contact.html" {
		t.Errorf("FilePath = %q, want simple-contact.html", ev.FilePath)
	}
	if ev.GenType != "chat" {
		t.Errorf("GenType = %q, want chat", ev.GenType)
	}
}
