package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/blamely/blamely/internal/authorship"
)

// captureWatcherAuthorship must feed a LIVE watcher edit into the working log (so
// hook-less tools are tracked), and SKIP a stale/replayed event (whose diff
// against the current file would mis-attribute).
func TestCaptureWatcherAuthorship(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "core.hooksPath="}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("checkout", "-q", "-b", "main")
	abs := filepath.Join(repo, "f.txt")
	os.WriteFile(abs, []byte("h1\nh2\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "c1")

	authorsByLine := func() map[int]authorship.AuthorType {
		ctx, ok := authorship.ResolveContext(abs)
		if !ok {
			t.Fatal("resolve ctx")
		}
		wl, err := authorship.LoadWorkingLog(ctx.RepoRoot, ctx.Branch, ctx.BaseSHA, ctx.RelPath)
		if err != nil || wl == nil {
			return nil
		}
		m := map[int]authorship.AuthorType{}
		for _, r := range wl.Lines {
			for ln := r.Start; ln <= r.End; ln++ {
				m[ln] = r.Author.Type
			}
		}
		return m
	}

	// LIVE watcher edit: AI appended line 3.
	os.WriteFile(abs, []byte("h1\nh2\nai3\n"), 0o644)
	captureWatcherAuthorship(Event{When: time.Now(), Tool: "copilot", GenType: "chat", RepoPath: repo, FilePath: "f.txt"})
	got := authorsByLine()
	if got[1] != authorship.Human || got[2] != authorship.Human || got[3] != authorship.AI {
		t.Fatalf("live: want H,H,AI; got %v", got)
	}

	// STALE replay: file changed again, but the event is old → must be skipped, so
	// line 4 is NOT captured as AI.
	os.WriteFile(abs, []byte("h1\nh2\nai3\nx4\n"), 0o644)
	captureWatcherAuthorship(Event{When: time.Now().Add(-time.Hour), Tool: "copilot", GenType: "chat", RepoPath: repo, FilePath: "f.txt"})
	if g := authorsByLine(); g[4] == authorship.AI {
		t.Errorf("stale replay should have been skipped, but line 4 was captured as AI: %v", g)
	}
}
