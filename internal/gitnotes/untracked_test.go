package gitnotes

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// IsUntracked must report true for a brand-new file (absent from HEAD and the index),
// false once it's staged, and false for ignored files. This is the gate that lets the
// gutter show AI attribution on creation: untracked files never appear in
// `git diff HEAD`, so UncommittedAddedLines is empty and callers fall back to "every
// line changed" only when IsUntracked agrees.
func TestIsUntracked(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "core.hooksPath="}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	git("checkout", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "seed.txt"), []byte("x\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "c1")

	// New file Copilot just wrote, not yet staged → untracked.
	os.WriteFile(filepath.Join(repo, "new.ts"), []byte("a\nb\n"), 0o644)
	if !IsUntracked(repo, "new.ts") {
		t.Fatal("expected new.ts to be untracked before git add")
	}

	// Staged → no longer untracked (it now appears in `git diff HEAD`).
	git("add", "new.ts")
	if IsUntracked(repo, "new.ts") {
		t.Fatal("expected new.ts to be tracked after git add")
	}

	// Committed, unchanged file → not untracked.
	if IsUntracked(repo, "seed.txt") {
		t.Fatal("expected committed seed.txt to be tracked")
	}

	// Ignored file → excluded by --exclude-standard, treated as not untracked so the
	// gutter stays blank (matches the plugins' V1 untracked handling).
	os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("ignored.log\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "ignored.log"), []byte("noise\n"), 0o644)
	if IsUntracked(repo, "ignored.log") {
		t.Fatal("expected ignored.log to be excluded by --exclude-standard")
	}

	// Nonexistent path → not untracked (nothing to attribute).
	if IsUntracked(repo, "does-not-exist.txt") {
		t.Fatal("expected missing file to report not-untracked")
	}
}
