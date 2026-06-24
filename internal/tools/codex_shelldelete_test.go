package tools

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestCodexShellCommandDeletion verifies the daemon watcher detects a file
// deletion Codex performs through its `shell_command` tool (the name current
// Codex builds emit for every shell exec — e.g. `Remove-Item` on Windows).
// Regression: shell_command was missing from codexShellNames, so the deletion
// was silently dropped and fell to Human at commit.
func TestCodexShellCommandDeletion(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) {
		c := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "t@t.t")
	git("config", "user.name", "t")

	target := filepath.Join(repo, "simple-login.html")
	if err := os.WriteFile(target, []byte("<html>\n<body>login</body>\n</html>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-qm", "add login")

	// Codex removed the file via the shell, so it's already gone from disk.
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}

	sink := &mockSink{}
	st := &codexState{sink: sink, model: "gpt-5.5"}

	// response_item / function_call / name="shell_command", arguments a
	// JSON-string-of-JSON (as Codex encodes it) carrying the Remove-Item command.
	inner, _ := json.Marshal(map[string]string{
		"command": "Remove-Item -LiteralPath ./simple-login.html",
		"workdir": repo,
	})
	payload, _ := json.Marshal(map[string]any{
		"type":      "function_call",
		"name":      "shell_command",
		"arguments": string(inner), // string-of-JSON
	})
	line, _ := json.Marshal(map[string]any{
		"timestamp": "2026-06-24T10:11:00.000Z",
		"type":      "response_item",
		"payload":   json.RawMessage(payload),
	})

	processCodexLine(line, st)
	if len(st.pending) != 1 {
		t.Fatalf("expected 1 buffered deletion event, got %d", len(st.pending))
	}
	st.flush(0, 0, 0, 0, false)

	if len(sink.events) != 1 {
		t.Fatalf("expected 1 recorded event, got %d", len(sink.events))
	}
	ev := sink.events[0]
	if ev.Tool != "codex" {
		t.Errorf("tool = %q, want codex", ev.Tool)
	}
	if ev.FilePath != "simple-login.html" {
		t.Errorf("file = %q, want simple-login.html", ev.FilePath)
	}
	if len(ev.RemovedLines) == 0 {
		t.Errorf("expected removed-line hashes for the deleted file, got none")
	}
	if ev.RepoPath == "" {
		t.Errorf("RepoPath empty -> event would be dropped by the sink")
	}
}
