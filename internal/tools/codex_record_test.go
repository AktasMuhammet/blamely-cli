package tools

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestExtractCodexHookRanges_ClaudeCompatibleWriteReturnsFullContent verifies
// that a Claude-compatible "Write" tool call (some Codex versions emit these)
// passes through extractClaudeRanges' 5th return value — the full new file
// content, used as the snapshot-diff fallback's input since Write carries no
// "before" text of its own.
func TestExtractCodexHookRanges_ClaudeCompatibleWriteReturnsFullContent(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"file_path": "/tmp/w.go",
		"content":   "package main\n",
	})
	p := codexHookPayload{ToolName: "Write", ToolInput: raw}
	_, _, _, _, newFullContent := extractCodexHookRanges(p)
	if newFullContent == nil || *newFullContent != "package main\n" {
		t.Errorf("newFullContent: want %q, got %v", "package main\n", newFullContent)
	}
}

// TestExtractCodexHookRanges_ClaudeCompatibleEditReturnsNilFullContent
// verifies that a Claude-compatible "Edit" tool call (explicit before/after
// text) leaves the 5th return value nil — only Write needs the snapshot
// fallback.
func TestExtractCodexHookRanges_ClaudeCompatibleEditReturnsNilFullContent(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"file_path":  "/tmp/x.go",
		"old_string": "a\nb\nc\nd",
		"new_string": "",
	})
	p := codexHookPayload{ToolName: "Edit", ToolInput: raw}
	_, _, _, _, newFullContent := extractCodexHookRanges(p)
	if newFullContent != nil {
		t.Errorf("newFullContent: want nil, got %q", *newFullContent)
	}
}

// TestExtractCodexHookRanges_PatchReturnsNilFullContent verifies that the
// apply_patch path — which always provides a unified diff with explicit
// before/after content — leaves the 5th return value nil.
func TestExtractCodexHookRanges_PatchReturnsNilFullContent(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"input": "*** Begin Patch\n" +
			"*** Update File: /repo/main.go\n" +
			"@@\n" +
			"-old line\n" +
			"+new line\n" +
			"*** End Patch\n",
	})
	p := codexHookPayload{ToolName: "apply_patch", ToolInput: raw}
	filePath, _, _, _, newFullContent := extractCodexHookRanges(p)
	if filePath != "/repo/main.go" {
		t.Fatalf("filePath: want /repo/main.go, got %q", filePath)
	}
	if newFullContent != nil {
		t.Errorf("newFullContent: want nil, got %q", *newFullContent)
	}
}

// ── Codex shell-deletion attribution ─────────────────────────────────────────

func TestShellDeleteTargets(t *testing.T) {
	cases := []struct {
		cmd  string
		want []string
	}{
		{"rm login.html", []string{"login.html"}},
		{"rm -f home.html", []string{"home.html"}},
		{"git rm old.go", []string{"old.go"}},
		{"rm a.txt && echo done", []string{"a.txt"}},
		{"del page.html", []string{"page.html"}},                                 // Windows cmd
		{"erase old.txt", []string{"old.txt"}},                                   // Windows cmd
		{"Remove-Item .\\foo.html", []string{".\\foo.html"}},                     // PowerShell
		{`del C:\proj\web\register.html`, []string{`C:\proj\web\register.html`}}, // Windows absolute path
		{`Remove-Item C:\proj\login.html`, []string{`C:\proj\login.html`}},
		{"ls -la", nil}, // not a delete
		{"dir", nil},    // Windows list, not a delete
	}
	for _, c := range cases {
		got := shellDeleteTargets(c.cmd)
		if len(got) != len(c.want) {
			t.Errorf("shellDeleteTargets(%q) = %v, want %v", c.cmd, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("shellDeleteTargets(%q)[%d] = %q, want %q", c.cmd, i, got[i], c.want[i])
			}
		}
	}
}

// TestEmitCodexShellDeletions_FingerprintsRm drives a real Codex response_item
// exec_command (`rm login.html`) against a git repo where login.html exists at
// HEAD: it must buffer a codex event carrying the file's removed-line hashes.
func TestEmitCodexShellDeletions_FingerprintsRm(t *testing.T) {
	// Isolate git config so a setting on the CI runner's global/system config
	// (core.hooksPath, commit.gpgsign, includeIf, autocrlf, …) can't perturb
	// this repo and make the emit step's `git show HEAD:` behave differently
	// than it does locally — the cause of this test passing locally but flaking
	// in CI. emitCodexShellDeletions shells out to git with the ambient
	// environment, and t.Setenv updates the process env exec.Command inherits,
	// so this covers both the setup commands and the emit lookup. The lone
	// safe.directory=* entry also neutralises git's "dubious ownership" refusal
	// on runners whose temp dirs are owned by a different uid.
	cfgHome := t.TempDir()
	gitGlobal := filepath.Join(cfgHome, ".gitconfig")
	if err := os.WriteFile(gitGlobal, []byte("[safe]\n\tdirectory = *\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", cfgHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(cfgHome, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", gitGlobal)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	root := t.TempDir()
	run := func(a ...string) {
		cmd := exec.Command("git", append([]string{"-C", root}, a...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", a, err, out)
		}
	}
	run("init")
	run("config", "core.autocrlf", "false") // stable hashes across OSes
	if err := os.WriteFile(filepath.Join(root, "login.html"), []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "add login.html")
	_ = os.Remove(filepath.Join(root, "login.html")) // codex's rm already ran

	payload := json.RawMessage(`{"type":"function_call","name":"exec_command","arguments":{"cmd":"rm login.html","workdir":"` + root + `"}}`)
	st := &codexState{}
	emitCodexShellDeletions(payload, time.Now(), st)

	if len(st.pending) != 1 {
		t.Fatalf("want 1 buffered event, got %d", len(st.pending))
	}
	ev := st.pending[0]
	if ev.Tool != "codex" || ev.GenType != "cli" {
		t.Errorf("tool/gen = %q/%q, want codex/cli", ev.Tool, ev.GenType)
	}
	if ev.FilePath != "login.html" {
		t.Errorf("FilePath = %q, want login.html", ev.FilePath)
	}
	if len(ev.RemovedLines) != 3 {
		t.Errorf("RemovedLines = %d, want 3 (a,b,c)", len(ev.RemovedLines))
	}
	if len(ev.Lines) != 0 {
		t.Errorf("a deletion must record no added Lines, got %d", len(ev.Lines))
	}
}

// TestEmitCodexShellDeletions_IgnoresNonDelete confirms a non-delete shell
// command (or non-shell function) buffers nothing.
func TestEmitCodexShellDeletions_IgnoresNonDelete(t *testing.T) {
	st := &codexState{}
	emitCodexShellDeletions(json.RawMessage(`{"type":"function_call","name":"exec_command","arguments":{"cmd":"ls -la","workdir":"/tmp"}}`), time.Now(), st)
	emitCodexShellDeletions(json.RawMessage(`{"type":"function_call","name":"apply_patch","arguments":{}}`), time.Now(), st)
	if len(st.pending) != 0 {
		t.Errorf("non-delete commands should buffer nothing, got %d", len(st.pending))
	}
}
