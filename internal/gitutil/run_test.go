package gitutil

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestOutputReturnsStdout(t *testing.T) {
	dir := initRepo(t)
	// symbolic-ref, not rev-parse: initRepo leaves the branch unborn (no commit),
	// where rev-parse HEAD has nothing to resolve.
	out, err := Output(dir, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "main" {
		t.Fatalf("branch = %q, want %q", got, "main")
	}
}

func TestOutputPropagatesGitFailure(t *testing.T) {
	// Not a repo → git exits non-zero, and that must surface as an error rather
	// than an empty success (callers treat "" as real content in places).
	if _, err := Output(t.TempDir(), "rev-parse", "HEAD"); err == nil {
		t.Fatal("expected an error outside a repo")
	}
}

// The whole point of the bounded runner: a git that never returns must not pin its
// caller's goroutine. `cat-file --batch` reads requests from stdin until EOF, so
// handing it a pipe nothing ever writes to or closes reproduces the shape of hang
// the deadline exists for (index.lock contention, a stalled network mount).
func TestCommandDeadlineKillsAHungGit(t *testing.T) {
	dir := initRepo(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer r.Close()
	defer w.Close() // held open on purpose — the child's stdin never sees EOF

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	cmd := Command(ctx, dir, "cat-file", "--batch")
	cmd.Stdin = r

	start := time.Now()
	if _, err := cmd.Output(); err == nil {
		t.Fatal("expected the deadline to kill the hung git")
	}
	// Generous bound: asserting it returned AT ALL is the point. Without the
	// deadline (or without WaitDelay, if a descriptor kept the pipe open) this
	// blocks until the test binary's own timeout.
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("took %v to give up — the deadline did not take effect", elapsed)
	}
}

func TestCommandSetsWaitDelay(t *testing.T) {
	// Without WaitDelay, Wait still blocks on the stdout pipe after the context
	// kills the process — reintroducing the hang the deadline is there to prevent.
	if cmd := Command(context.Background(), t.TempDir(), "status"); cmd.WaitDelay <= 0 {
		t.Fatal("Command must set WaitDelay")
	}
}
