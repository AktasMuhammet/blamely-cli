package gitutil

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// Regression: RepoID/Toplevel must resolve for a path that does NOT exist on disk yet.
// An AI `create_file` (Copilot/Claude/etc.) streams its transcript event in real time,
// and the watcher can tail it BEFORE the new file is flushed to disk. If repo
// resolution required the file to exist, `git -C <missing-file>` failed, RepoID
// returned "", and the brand-new file's AI edit was dropped — the file then fell to
// Human at commit. Resolving via the parent directory fixes it.
func TestRepoID_ResolvesForNotYetWrittenFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	missing := filepath.Join(repo, "package.json") // never written to disk

	if id, ok := RepoID(missing); !ok || id == "" {
		t.Errorf("RepoID(not-yet-written) = (%q, %v); want a resolved repo", id, ok)
	}
	if top, ok := Toplevel(missing); !ok || top == "" {
		t.Errorf("Toplevel(not-yet-written) = (%q, %v); want the worktree root", top, ok)
	}
}
