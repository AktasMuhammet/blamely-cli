package gitnotes

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/blamely/blamely/internal/authorship"
)

// Regression guard for the "all-Human re-attribution" bug: a committed file's
// working log must SURVIVE the commit so a later re-attribution that has neither an
// inherited note nor an in-window SQLite edit (a divergent sibling commit on the same
// parent, or a recompute after the edits aged out of the window) can still re-flip
// the AI attribution from it. A prior change deleted the log on commit, forcing
// recovery onto the fragile SQLite content-sha window — when that window excluded the
// edit, the note silently shipped all-Human with no way back. See MigrateWorkingLogs.
func TestReattribution_RecoversFromKeptWorkingLog(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	git := func(a ...string) {
		c := exec.Command("git", append([]string{"-C", repo, "-c", "core.hooksPath="}, a...)...)
		c.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", a, err, out)
		}
	}
	rev := func() string {
		out, _ := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
		return string(out[:40])
	}
	git("init", "-q")
	// Set identity in the repo config (not just env) so AttributeAndWrite's internal
	// `git notes add` has an author identity on CI runners with no global config.
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	git("checkout", "-q", "-b", "main")
	git("commit", "-q", "--allow-empty", "-m", "c0")
	parent := rev()

	// AI authors a new file; the editor tracker records it in the working log.
	os.WriteFile(filepath.Join(repo, "app.py"), []byte("ai1\nai2\nai3\n"), 0o644)
	if _, err := authorship.Update(repo, "main", parent, "app.py", "ai1\nai2\nai3\n", "",
		authorship.Author{Type: authorship.AI, Tool: "claude", GenType: "chat"}, 1); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "c1")
	head := rev()

	// First attribution writes the note, then the post-commit migration runs.
	if _, err := AttributeAndWrite(repo, head); err != nil {
		t.Fatal(err)
	}

	// The committed file's working log must still exist at the parent base.
	wlPath := filepath.Join(repo, ".git", "blamely", "working_logs", "main", parent, "app.py.json")
	if _, err := os.Stat(wlPath); err != nil {
		t.Fatalf("committed file's working log was deleted post-commit (regression): %v", err)
	}

	// Re-attribute with NO inherited note and NO SQLite edit — the kept working log
	// is the only source. It must still resolve to AI, not collapse to Human.
	exec.Command("git", "-C", repo, "notes", "--ref=refs/notes/blamely", "remove", head).Run()
	note, err := AttributeAndWrite(repo, head)
	if err != nil {
		t.Fatal(err)
	}
	if note.Totals.AILines != 3 || note.Totals.HumanLines != 0 {
		t.Fatalf("re-attribution lost AI: ai=%d human=%d (want ai=3 human=0)",
			note.Totals.AILines, note.Totals.HumanLines)
	}
}
