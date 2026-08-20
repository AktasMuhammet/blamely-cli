package tools

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/blamely/blamely/internal/authorship"
)

// authorsByLine reads the file's working log as a line → author map.
func authorsByLine(t *testing.T, abs string) map[int]authorship.Author {
	t.Helper()
	ctx, ok := authorship.ResolveContext(abs)
	if !ok {
		t.Fatalf("resolve authorship context for %s", abs)
	}
	wl, err := authorship.LoadWorkingLog(ctx.RepoRoot, ctx.Branch, ctx.BaseSHA, ctx.RelPath)
	if err != nil || wl == nil {
		return nil
	}
	m := map[int]authorship.Author{}
	for _, r := range wl.Lines {
		for ln := r.Start; ln <= r.End; ln++ {
			m[ln] = r.Author
		}
	}
	return m
}

// Every agent that can write through a shell must land in the working log under
// its OWN tool name — the shared path is what keeps the four from drifting apart.
func TestRecordShellWritesIn_PerTool(t *testing.T) {
	for _, tool := range []string{"copilot", "gemini", "codex", "claude"} {
		t.Run(tool, func(t *testing.T) {
			repo, abs := gitRepoWithFile(t, "gen.py", "h1\nh2\n")
			if err := os.WriteFile(abs, []byte("h1\nh2\nai3\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			err := recordShellWritesIn(repo, "python3 gen.py", shellWriteOpts{
				Tool:         tool,
				GenType:      "cli",
				SessionID:    "sess-1",
				WriteSource:  tool + "_shell_fswrite",
				DeleteSource: tool + "_shell_delete",
			})
			if err != nil {
				t.Fatalf("recordShellWritesIn: %v", err)
			}
			got := authorsByLine(t, abs)[3]
			if got.Type != authorship.AI || got.Tool != tool {
				t.Errorf("line 3 = %+v, want AI/%s", got, tool)
			}
		})
	}
}

// isShellToolName must cover every agent's shell tool: a name we don't recognise
// means no pre-command baseline, which puts the user's own work at risk of being
// claimed by the agent (see captureShellBaselines).
func TestIsShellToolName(t *testing.T) {
	for _, name := range []string{
		"Bash", "bash", // Claude
		"run_in_terminal",                     // Copilot
		"run_shell_command", "Shell", "shell", // Gemini / Cursor
		"shell_command", "exec_command", "local_shell", "local_shell_call", "container.exec", // Codex
	} {
		if !isShellToolName(name) {
			t.Errorf("isShellToolName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"Write", "Edit", "apply_patch", "delete_file", ""} {
		if isShellToolName(name) {
			t.Errorf("isShellToolName(%q) = true, want false", name)
		}
	}
}

// withCommitSeedHook installs the SeedHook the git-notes layer registers in
// production, so a test exercises the REAL chain: seed-from-commit runs before the
// first recorded edit. Without it the seed never fires and a test can pass while
// the shipped binary fails — which is exactly how the baseline-clobbering bug in
// authorship.SeedWorkingLog got past an earlier version of these tests.
func withCommitSeedHook(t *testing.T, committed string, lines []authorship.LineAttribution) {
	t.Helper()
	prev := authorship.SeedHook
	authorship.SeedHook = func(repoRoot, branch, baseSHA, relPath string) {
		_ = authorship.SeedWorkingLog(repoRoot, branch, baseSHA, relPath, committed, lines, 0)
	}
	t.Cleanup(func() { authorship.SeedHook = prev })
}

// The honesty invariant, through the full production chain (commit seed + pre-edit
// baseline + shell write): the command claims what IT changed, and the user's
// unobserved uncommitted line stays Human.
func TestShellWrite_WithCommitSeed_LeavesPreExistingWorkHuman(t *testing.T) {
	repo, abs := gitRepoWithFile(t, "app.py", "committed1\ncommitted2\n")
	withCommitSeedHook(t, "committed1\ncommitted2\n", []authorship.LineAttribution{
		{Start: 1, End: 2, Author: authorship.Author{Type: authorship.Human, GenType: "human"}},
	})

	if err := os.WriteFile(abs, []byte("committed1\ncommitted2\nhuman3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	payload := fmt.Sprintf(`{"cwd":%q,"tool_name":"Bash","tool_input":{"command":"python3 gen.py"}}`, repo)
	if err := CaptureBaselineFromStdin(bytes.NewReader([]byte(payload))); err != nil {
		t.Fatalf("CaptureBaselineFromStdin: %v", err)
	}
	if err := os.WriteFile(abs, []byte("committed1\ncommitted2\nhuman3\nai4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := recordShellWritesIn(repo, "python3 gen.py", shellWriteOpts{
		Tool: "claude", GenType: "cli", SessionID: "s", WriteSource: "claude_bash_fswrite",
	}); err != nil {
		t.Fatalf("recordShellWritesIn: %v", err)
	}

	got := authorsByLine(t, abs)
	if a := got[4]; a.Type != authorship.AI || a.Tool != "claude" {
		t.Errorf("line 4 = %+v, want AI/claude (the command wrote it)", a)
	}
	if a := got[3]; a.Type == authorship.AI {
		t.Errorf("line 3 = %+v, want Human: the user typed it before the command ran, "+
			"and the commit seed must not have clobbered the captured baseline", a)
	}
}

// The honesty invariant: a shell write must claim what the COMMAND changed, not
// every uncommitted change the file had accumulated. Without a pre-command
// baseline the first observation diffs against HEAD and claims the user's own
// untracked work too.
func TestCaptureShellBaselines_LeavesPreExistingWorkHuman(t *testing.T) {
	repo, abs := gitRepoWithFile(t, "app.py", "committed1\ncommitted2\n")

	// The user edited by hand in an editor Blamely isn't watching: no working log,
	// no baseline, the file simply differs from HEAD.
	if err := os.WriteFile(abs, []byte("committed1\ncommitted2\nhuman3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// PreToolUse for a shell command → snapshot the pre-command state.
	payload := fmt.Sprintf(`{"cwd":%q,"tool_name":"run_in_terminal","tool_input":{"command":"python3 gen.py"}}`, repo)
	if err := CaptureBaselineFromStdin(bytes.NewReader([]byte(payload))); err != nil {
		t.Fatalf("CaptureBaselineFromStdin: %v", err)
	}

	// The command then appends its own line.
	if err := os.WriteFile(abs, []byte("committed1\ncommitted2\nhuman3\nai4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := recordShellWritesIn(repo, "python3 gen.py", shellWriteOpts{
		Tool: "copilot", GenType: "chat", SessionID: "s", WriteSource: "copilot_shell_fswrite",
	}); err != nil {
		t.Fatalf("recordShellWritesIn: %v", err)
	}

	got := authorsByLine(t, abs)
	if a := got[4]; a.Type != authorship.AI || a.Tool != "copilot" {
		t.Errorf("line 4 = %+v, want AI/copilot (the command wrote it)", a)
	}
	if a := got[3]; a.Type == authorship.AI {
		t.Errorf("line 3 = %+v, want Human: the user typed it before the command ran", a)
	}
}

// The same scenario WITHOUT the pre-command snapshot: the first observation can
// only diff against HEAD, so the user's line is swept in. This pins the failure
// the snapshot exists to prevent — if this ever starts passing as Human, the
// snapshot is no longer the only thing protecting it.
func TestShellWrite_WithoutBaseline_ClaimsPreExistingWork(t *testing.T) {
	repo, abs := gitRepoWithFile(t, "app.py", "committed1\ncommitted2\n")
	if err := os.WriteFile(abs, []byte("committed1\ncommitted2\nhuman3\nai4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := recordShellWritesIn(repo, "python3 gen.py", shellWriteOpts{
		Tool: "copilot", GenType: "chat", SessionID: "s", WriteSource: "copilot_shell_fswrite",
	}); err != nil {
		t.Fatalf("recordShellWritesIn: %v", err)
	}
	if a := authorsByLine(t, abs)[3]; a.Type != authorship.AI {
		t.Skipf("line 3 = %+v: no longer over-claimed without a baseline, so the "+
			"HEAD-fallback gap has been closed elsewhere; revisit this test", a)
	}
}

// captureShellBaselines must NOT overwrite the baseline of a file Attribution is
// already tracking: that baseline is what the file's current line attributions
// describe, and replacing it would strand their line numbers.
func TestCaptureShellBaselines_SkipsTrackedFiles(t *testing.T) {
	repo, abs := gitRepoWithFile(t, "app.py", "c1\nc2\n")

	// First observed edit → the file now HAS a working log and a baseline.
	if err := os.WriteFile(abs, []byte("c1\nc2\nai3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := recordShellWritesIn(repo, "python3 gen.py", shellWriteOpts{
		Tool: "claude", GenType: "cli", SessionID: "s", WriteSource: "claude_bash_fswrite",
	}); err != nil {
		t.Fatal(err)
	}
	if a := authorsByLine(t, abs)[3]; a.Type != authorship.AI {
		t.Fatalf("precondition: line 3 = %+v, want AI", a)
	}

	// A later shell command's pre-hook must leave that state alone.
	captureShellBaselines(repo)
	if a := authorsByLine(t, abs)[3]; a.Type != authorship.AI || a.Tool != "claude" {
		t.Errorf("line 3 = %+v after captureShellBaselines, want the AI/claude attribution kept", a)
	}
}

// A shell payload with no cwd must be a no-op rather than scanning from wherever
// the hook happens to be running.
func TestCaptureShellBaselines_NoCwd(t *testing.T) {
	captureShellBaselines("")
	payload := `{"tool_name":"Bash","tool_input":{"command":"ls"}}`
	if err := CaptureBaselineFromStdin(bytes.NewReader([]byte(payload))); err != nil {
		t.Errorf("CaptureBaselineFromStdin with no cwd: %v", err)
	}
}

// A file the pre-hook snapshotted keeps its baseline next to the working log, so
// the paths line up with what the engine reads back.
func TestCaptureShellBaselines_WritesBaselineForUntrackedFile(t *testing.T) {
	repo, abs := gitRepoWithFile(t, "app.py", "c1\n")
	if err := os.WriteFile(abs, []byte("c1\nhuman2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	captureShellBaselines(repo)

	ctx, ok := authorship.ResolveContext(abs)
	if !ok {
		t.Fatal("resolve ctx")
	}
	base := authorship.BaselinePath(ctx.RepoRoot, ctx.Branch, ctx.BaseSHA, ctx.RelPath)
	data, err := os.ReadFile(base)
	if err != nil {
		t.Fatalf("baseline not written at %s: %v", filepath.Base(base), err)
	}
	if string(data) != "c1\nhuman2\n" {
		t.Errorf("baseline = %q, want the pre-command content", data)
	}
}
