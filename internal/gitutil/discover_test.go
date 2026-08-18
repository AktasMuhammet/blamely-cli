package gitutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// initRepoAt makes `dir` a git repo (unlike initRepo, which picks its own temp
// dir) so a test can lay out several repos under one workspace parent.
func initRepoAt(t *testing.T, dir string) string {
	t.Helper()
	requireGit(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", "-b", "main", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v\n%s", dir, err, out)
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	return dir
}

func tempDirResolved(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	return dir
}

// Inside a work tree the answer must be exactly that repo — the historical
// single-repo behaviour every caller relies on.
func TestDiscoverReposInsideRepo(t *testing.T) {
	repo := initRepo(t)
	sub := filepath.Join(repo, "pkg", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, from := range []string{repo, sub} {
		got := DiscoverRepos(from)
		if len(got) != 1 || got[0] != repo {
			t.Fatalf("DiscoverRepos(%q) = %v, want [%q]", from, got, repo)
		}
	}
}

// The case this exists for: the CLI/agent is started in a workspace dir that is
// NOT a repo, holding sibling clones. `git rev-parse` finds nothing there.
func TestDiscoverReposFromWorkspaceParent(t *testing.T) {
	ws := tempDirResolved(t)
	backend := initRepoAt(t, filepath.Join(ws, "backend"))
	frontend := initRepoAt(t, filepath.Join(ws, "frontend"))

	if _, ok := Toplevel(ws); ok {
		t.Fatal("workspace parent unexpectedly resolves as a repo")
	}
	got := DiscoverRepos(ws)
	want := []string{backend, frontend} // sorted
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("DiscoverRepos(%q) = %v, want %v", ws, got, want)
	}
}

// A grouping level between the workspace and the repos is still found.
func TestDiscoverReposNestedDepth(t *testing.T) {
	ws := tempDirResolved(t)
	api := initRepoAt(t, filepath.Join(ws, "services", "api"))
	got := DiscoverRepos(ws)
	if len(got) != 1 || got[0] != api {
		t.Fatalf("DiscoverRepos(%q) = %v, want [%q]", ws, got, api)
	}
}

// Dependency/build trees are never descended into: a repo vendored inside
// node_modules is not the user's project, and walking it is expensive.
func TestDiscoverReposSkipsHeavyDirs(t *testing.T) {
	ws := tempDirResolved(t)
	app := initRepoAt(t, filepath.Join(ws, "app"))
	initRepoAt(t, filepath.Join(ws, "node_modules", "some-dep"))
	initRepoAt(t, filepath.Join(ws, ".cache", "hidden-clone"))

	got := DiscoverRepos(ws)
	if len(got) != 1 || got[0] != app {
		t.Fatalf("DiscoverRepos(%q) = %v, want [%q]", ws, got, app)
	}
}

// A repo nested INSIDE a work tree is a submodule — covered by its parent, so
// the scan stops at the outer repo rather than reporting both.
func TestDiscoverReposStopsAtRepoBoundary(t *testing.T) {
	ws := tempDirResolved(t)
	outer := initRepoAt(t, filepath.Join(ws, "outer"))
	initRepoAt(t, filepath.Join(outer, "inner"))

	got := DiscoverRepos(ws)
	if len(got) != 1 || got[0] != outer {
		t.Fatalf("DiscoverRepos(%q) = %v, want [%q]", ws, got, outer)
	}
}

// Neither in a repo nor above one: nil, so callers can still report the honest
// "not inside a git repository".
func TestDiscoverReposNone(t *testing.T) {
	requireGit(t)
	ws := tempDirResolved(t)
	if err := os.MkdirAll(filepath.Join(ws, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := DiscoverRepos(ws); got != nil {
		t.Fatalf("DiscoverRepos(%q) = %v, want nil", ws, got)
	}
	if got := DiscoverRepos(""); got != nil {
		t.Fatalf("DiscoverRepos(\"\") = %v, want nil", got)
	}
}
