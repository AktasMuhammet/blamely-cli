package gitutil

import (
	"context"
	"os/exec"
	"time"

	"github.com/blamely/blamely/internal/procattr"
)

// DefaultTimeout bounds a git invocation made from long-running code — the daemon
// and its watchers, where nothing ever restarts the process.
//
// git answers these in milliseconds normally; the cases that don't are exactly the
// ones worth killing: index.lock contention with a concurrent git, a stalled
// network filesystem, a pathologically large working tree. Without a deadline one
// of those blocks its watcher goroutine for the life of the daemon, and the watcher
// simply stops reporting edits with nothing in the log to say why.
//
// Deliberately generous: this is a wedge-preventer, not a latency budget. A
// `git status --untracked-files=all` over a monorepo on a cold cache can honestly
// take many seconds, and killing that would lose real attribution data.
const DefaultTimeout = 30 * time.Second

// waitDelay caps how long Wait blocks after the context kills the process. Without
// it, Output still waits on the stdout pipe, which a grandchild holding the
// descriptor open would keep from ever closing — reintroducing the hang the
// deadline exists to prevent.
const waitDelay = 5 * time.Second

// Output runs `git -C dir args...` under DefaultTimeout and returns its stdout.
// Drop-in for procattr.Hide(exec.Command("git", "-C", dir, ...)).Output() in daemon-resident code.
func Output(dir string, args ...string) ([]byte, error) {
	return OutputTimeout(DefaultTimeout, dir, args...)
}

// OutputTimeout is Output with an explicit deadline, for callers that know their
// command should be much faster (or slower) than the default.
func OutputTimeout(d time.Duration, dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return Command(ctx, dir, args...).Output()
}

// Command builds a bounded `git -C dir args...`. Use it when the caller needs to
// set up the command further (stdin, extra env) before running it; the context
// kills the process, and WaitDelay keeps Wait from outliving it.
func Command(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := procattr.Hide(exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...))
	cmd.WaitDelay = waitDelay
	return cmd
}
