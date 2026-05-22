package gitutil

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// RepoID returns the canonical repository identifier for the file/dir at `p`.
//
// Why not just `git rev-parse --show-toplevel`? With linked worktrees, every
// worktree has its OWN top-level dir, so edits recorded under worktree A
// can't be joined against commits in worktree B even though they share the
// same logical repository.
//
// `git rev-parse --git-common-dir` returns the .git directory of the MAIN
// worktree (the same for every linked worktree). Stripping the trailing
// `/.git` gives us a stable repo path. Symlinks are resolved so macOS
// `/tmp → /private/tmp` doesn't sneak in here.
//
// Returns ("", false) if the path isn't inside a git repo (or git failed).
func RepoID(p string) (string, bool) {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	dir := p
	if fi, err := pathStat(p); err == nil && !fi.IsDir() {
		dir = filepath.Dir(p)
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return "", false
	}
	gitDir := strings.TrimSpace(string(out))
	if gitDir == "" {
		return "", false
	}
	if r, err := filepath.EvalSymlinks(gitDir); err == nil {
		gitDir = r
	}
	// Strip the trailing "/.git" segment if present (the common dir for a
	// regular clone is .../<repo>/.git; for a bare repo it's just .../<repo>).
	if filepath.Base(gitDir) == ".git" {
		return filepath.Dir(gitDir), true
	}
	return gitDir, true
}

// Toplevel returns the working-tree root for `p`. Useful when callers
// genuinely want the worktree-local path (e.g. computing relative file
// paths for the diff parser).
func Toplevel(p string) (string, bool) {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	dir := p
	if fi, err := pathStat(p); err == nil && !fi.IsDir() {
		dir = filepath.Dir(p)
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", false
	}
	root := strings.TrimSpace(string(out))
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	return root, true
}
