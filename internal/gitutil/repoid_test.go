package gitutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// requireGit skips the test if `git` isn't on PATH. The package shells out to
// git for every public function, so without git there's nothing to verify.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed: " + err.Error())
	}
}

// initRepo creates a regular (non-bare) git repo at a fresh temp dir and
// returns its path with symlinks resolved (matches what RepoID/Toplevel
// would return when called from inside).
func initRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	cmd := exec.Command("git", "init", "-q", "-b", "main", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return dir
}

// commitFile creates and commits `name` so the repo has a HEAD — required
// before `git worktree add` can succeed.
func commitFile(t *testing.T, repo, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// Use -c overrides so the test doesn't depend on the developer having
	// user.email/user.name configured globally.
	run := func(args ...string) {
		base := []string{"-C", repo,
			"-c", "user.email=test@blamely.test",
			"-c", "user.name=Blamely Test",
			"-c", "commit.gpgsign=false",
		}
		cmd := exec.Command("git", append(base, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("add", name)
	run("commit", "-q", "-m", "seed")
}

func TestRepoID_FromRepoRoot(t *testing.T) {
	repo := initRepo(t)
	got, ok := RepoID(repo)
	if !ok {
		t.Fatal("expected ok=true for a git repo")
	}
	if got != repo {
		t.Errorf("RepoID = %q, want %q", got, repo)
	}
}

func TestRepoID_FromSubdirectory(t *testing.T) {
	repo := initRepo(t)
	sub := filepath.Join(repo, "pkg", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, ok := RepoID(sub)
	if !ok {
		t.Fatal("expected ok=true from subdirectory")
	}
	if got != repo {
		t.Errorf("RepoID = %q, want %q (repo root)", got, repo)
	}
}

func TestRepoID_FromFilePath(t *testing.T) {
	// File paths (not just directories) should resolve to the repo root —
	// callers pass file paths into RepoID all the time.
	repo := initRepo(t)
	file := filepath.Join(repo, "hello.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := RepoID(file)
	if !ok {
		t.Fatal("expected ok=true for file in repo")
	}
	if got != repo {
		t.Errorf("RepoID(file) = %q, want %q", got, repo)
	}
}

func TestRepoID_NotInRepo(t *testing.T) {
	requireGit(t)
	// A fresh temp dir with no .git anywhere up the tree.
	tmp := t.TempDir()
	if _, ok := RepoID(tmp); ok {
		t.Error("expected ok=false outside any git repo")
	}
}

func TestRepoID_StableAcrossLinkedWorktrees(t *testing.T) {
	// The whole motivation behind using --git-common-dir is that linked
	// worktrees report the SAME RepoID as the main worktree, even though
	// `git rev-parse --show-toplevel` differs between them.
	if runtime.GOOS == "windows" {
		// Worktree creation on Windows CI is occasionally flaky around symlinks.
		// The POSIX assertion covers the contract we care about.
		t.Skip("linked-worktree test is POSIX-only")
	}
	repo := initRepo(t)
	commitFile(t, repo, "seed.txt", "seed")

	// Place the linked worktree OUTSIDE the main repo so it can't be confused
	// with a subdirectory of the main checkout.
	parent := filepath.Dir(repo)
	worktree := filepath.Join(parent, "linked-"+filepath.Base(repo))
	cmd := exec.Command("git", "-C", repo, "worktree", "add", "-q", worktree)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}
	defer exec.Command("git", "-C", repo, "worktree", "remove", "-f", worktree).Run()

	mainID, ok := RepoID(repo)
	if !ok {
		t.Fatal("RepoID failed on main worktree")
	}
	linkedID, ok := RepoID(worktree)
	if !ok {
		t.Fatal("RepoID failed on linked worktree")
	}
	if mainID != linkedID {
		t.Errorf("RepoID differs across worktrees: main=%q linked=%q", mainID, linkedID)
	}

	// Sanity: Toplevel should NOT agree across linked worktrees — that's the
	// behavior RepoID exists to paper over.
	mainTop, _ := Toplevel(repo)
	linkedTop, _ := Toplevel(worktree)
	if mainTop == linkedTop {
		t.Errorf("Toplevel unexpectedly identical: %q (would defeat RepoID's purpose)", mainTop)
	}
}

func TestToplevel_ReturnsRoot(t *testing.T) {
	repo := initRepo(t)
	sub := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, ok := Toplevel(sub)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got != repo {
		t.Errorf("Toplevel = %q, want %q", got, repo)
	}
}

func TestToplevel_NotInRepo(t *testing.T) {
	requireGit(t)
	if _, ok := Toplevel(t.TempDir()); ok {
		t.Error("expected ok=false outside any git repo")
	}
}
