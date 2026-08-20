package tools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/blamely/blamely/internal/authorship"
)

// gitRepoWithFile makes a repo with one committed file and returns (repo, abs).
func gitRepoWithFile(t *testing.T, name, content string) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "core.hooksPath="}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("checkout", "-q", "-b", "main")
	abs := filepath.Join(repo, name)
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "c1")
	return repo, abs
}

// writeBashTranscript writes a one-entry Claude transcript whose Bash tool_use is
// stamped `ago` in the past, i.e. a command that has been running that long.
func writeBashTranscript(t *testing.T, ago time.Duration) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	line := fmt.Sprintf(
		`{"timestamp":%q,"message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"python gen.py"}}]}}`+"\n",
		time.Now().Add(-ago).Format(time.RFC3339Nano))
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// A file written by a shell command must land in the Attribution WORKING LOG, not
// only in the daemon's DB. The commit-time flip reads the working log, so a
// DB-only record leaves the line unobserved and it falls back to Human — the
// regression this test pins.
func TestRecordClaudeBashWrites_MirrorsToWorkingLog(t *testing.T) {
	repo, abs := gitRepoWithFile(t, "gen.py", "h1\nh2\n")

	// The shell command appended a line, as `python3 - <<PYEOF` would.
	if err := os.WriteFile(abs, []byte("h1\nh2\nai3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Best-effort by contract: no daemon runs in tests and a hook that errors can
	// abort the host's whole hook chain.
	if err := recordClaudeBashWritesIn(repo, "sess-1", "", "cli", "python3 gen.py"); err != nil {
		t.Fatalf("recordClaudeBashWritesIn: %v", err)
	}

	ctx, ok := authorship.ResolveContext(abs)
	if !ok {
		t.Fatal("resolve authorship context")
	}
	wl, err := authorship.LoadWorkingLog(ctx.RepoRoot, ctx.Branch, ctx.BaseSHA, ctx.RelPath)
	if err != nil {
		t.Fatalf("load working log: %v", err)
	}
	if wl == nil {
		t.Fatal("no working log written: the shell write was never mirrored into Attribution")
	}
	byLine := map[int]authorship.Author{}
	for _, r := range wl.Lines {
		for ln := r.Start; ln <= r.End; ln++ {
			byLine[ln] = r.Author
		}
	}
	if got := byLine[3]; got.Type != authorship.AI || got.Tool != "claude" {
		t.Errorf("line 3 = %+v, want AI/claude", got)
	}
	// The two pre-existing lines are untouched by this command and must keep
	// falling back to Human — the fix must not sweep the whole file into AI.
	for _, ln := range []int{1, 2} {
		if got := byLine[ln]; got.Type == authorship.AI {
			t.Errorf("line %d = %+v, want Human (the command did not change it)", ln, got)
		}
	}
}

// bashWriteWindowFor must cover the command's whole run, so a file written at the
// start of a long command is still credited when the hook fires at its exit.
func TestBashWriteWindowFor(t *testing.T) {
	if got := bashWriteWindowFor(""); got != bashWriteWindow {
		t.Errorf("no transcript: got %v, want the %v floor", got, bashWriteWindow)
	}
	if got := bashWriteWindowFor(filepath.Join(t.TempDir(), "missing.jsonl")); got != bashWriteWindow {
		t.Errorf("missing transcript: got %v, want the %v floor", got, bashWriteWindow)
	}
	// 45s command (black + pytest): the window must reach past its start.
	if got := bashWriteWindowFor(writeBashTranscript(t, 45*time.Second)); got < 45*time.Second {
		t.Errorf("45s command: got %v, want >= 45s", got)
	}
	// A future timestamp (clock skew) must not widen anything.
	if got := bashWriteWindowFor(writeBashTranscript(t, -time.Hour)); got != bashWriteWindow {
		t.Errorf("future timestamp: got %v, want the %v floor", got, bashWriteWindow)
	}
	// A stale transcript must clamp, not open the window across the session.
	if got := bashWriteWindowFor(writeBashTranscript(t, 24*time.Hour)); got != maxBashWriteWindow {
		t.Errorf("stale transcript: got %v, want the %v ceiling", got, maxBashWriteWindow)
	}
}

// The customer-reported shape: a command that writes files and THEN runs a
// formatter and the test suite exits long after the write, so the old fixed 15s
// window dropped the file entirely.
func TestClaudeBashWritePayloads_LongRunningCommand(t *testing.T) {
	repo, abs := gitRepoWithFile(t, "health.py", "h1\nh2\n")
	if err := os.WriteFile(abs, []byte("h1\nh2\nai3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The write happened 40s ago; the command only exited now.
	wrote := time.Now().Add(-40 * time.Second)
	if err := os.Chtimes(abs, wrote, wrote); err != nil {
		t.Fatal(err)
	}

	// Without a transcript we can't know the command's duration, so the floor
	// applies and the file is (still) missed — the pre-fix behaviour.
	if got := claudeBashWritePayloads(repo, "sess-1", "", "cli"); len(got) != 0 {
		t.Errorf("no transcript: got %d payloads, want 0 (15s floor excludes a 40s-old write)", len(got))
	}
	// With the transcript, the window covers the command's 45s run and the file
	// is credited.
	tr := writeBashTranscript(t, 45*time.Second)
	got := claudeBashWritePayloads(repo, "sess-1", tr, "cli")
	if len(got) != 1 || got[0].FilePath != "health.py" {
		t.Fatalf("got %+v, want one payload for health.py", got)
	}
	if got[0].Tool != "claude" {
		t.Errorf("tool = %q, want claude", got[0].Tool)
	}
}
