package report

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/blamely/blamely/internal/gitutil"
)

// errNotInRepo is returned when the cwd is neither inside a git work tree nor
// above any — the only case where a repo-scoped report genuinely has nothing to
// report on.
var errNotInRepo = errors.New("not inside a git repository")

// currentRepoRoots resolves the repositories a repo-scoped command applies to.
//
// Normally that is the single repo containing the cwd. But an agent (and a
// developer) is often started one level ABOVE the repos — a workspace directory
// whose `backend/` and `frontend/` are separate clones. `git rev-parse` only ever
// searches upward, so every report run from there used to fail with "not inside a
// git repository" even though the edits were captured and stored correctly.
// DiscoverRepos looks downward too, and the callers below render each repo.
func currentRepoRoots() ([]string, error) {
	roots := gitutil.DiscoverRepos(".")
	if len(roots) == 0 {
		return nil, errNotInRepo
	}
	return roots, nil
}

// currentRepoIDs is currentRepoRoots mapped through RepoID — the canonical
// identifier rows are stored under (worktree-stable), for DB queries.
func currentRepoIDs() ([]string, error) {
	roots, err := currentRepoRoots()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(roots))
	for _, root := range roots {
		id, ok := gitutil.RepoID(root)
		if !ok || id == "" {
			id = root
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// repoBanner names the repo a following block belongs to. Printed only when a
// command spans more than one repo, so single-repo output is byte-for-byte what
// it has always been.
func repoBanner(w io.Writer, root string, multi bool) {
	if !multi {
		return
	}
	fmt.Fprintf(w, "\n%s\n", bold(filepath.Base(root)+"/")+dim("  "+root))
}

// repoRootForFile resolves the repo that owns `file` — for commands whose
// argument already names a location, which is more precise than the cwd (the
// file may live in a sibling repo under a shared workspace dir). Falls back to
// the cwd's repo when the path can't be resolved.
func repoRootForFile(file string) (string, error) {
	abs := file
	if !filepath.IsAbs(abs) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		abs = filepath.Join(cwd, file)
	}
	if root, ok := gitutil.Toplevel(filepath.Dir(abs)); ok {
		return root, nil
	}
	if root, ok := gitutil.Toplevel("."); ok {
		return root, nil
	}
	return "", errNotInRepo
}
