package authorship

import (
	"os"
	"os/exec"
	"path/filepath"
)

// GCWorkingLogs prunes working-log trees whose base commit git no longer has —
// i.e. base_sha directories whose object is gone after an amend / rebase / `git gc`
// removed the dangling commit. This is provably safe: a log is removed ONLY once
// its base object no longer exists, so it can never describe content reachable from
// any ref. It bounds the disk growth from history-rewriting churn.
//
// It does NOT prune logs whose base is still a valid object (even if unreachable):
// those may back uncommitted edits in another worktree/branch. The broader
// lifecycle — carrying committed authorship across a commit as the next log's prior
// (note-seeding) — is separate; without it, GC here never risks a regression.
//
// Best-effort and cross-platform (path/filepath + git plumbing only). Returns the
// number of base_sha directories removed.
func GCWorkingLogs(repoRoot string) (pruned int, err error) {
	root := filepath.Join(repoRoot, ".git", "blamely", "working_logs")
	branchDirs, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	for _, bd := range branchDirs {
		if !bd.IsDir() {
			continue
		}
		branchPath := filepath.Join(root, bd.Name())
		baseDirs, derr := os.ReadDir(branchPath)
		if derr != nil {
			continue
		}
		for _, sd := range baseDirs {
			if !sd.IsDir() || !looksLikeSHA(sd.Name()) {
				continue // keep non-SHA bases (INITIAL/DETACHED) and stray files
			}
			if objectExists(repoRoot, sd.Name()) {
				continue // base object still present → keep (conservative)
			}
			if os.RemoveAll(filepath.Join(branchPath, sd.Name())) == nil {
				pruned++
			}
		}
		// Drop the branch dir if GC emptied it.
		if entries, rerr := os.ReadDir(branchPath); rerr == nil && len(entries) == 0 {
			_ = os.Remove(branchPath)
		}
	}
	return pruned, nil
}

// looksLikeSHA reports whether s is a 40- or 64-hex-char object id (SHA-1 / SHA-256),
// so GC only ever considers real base_sha directories — never INITIAL / DETACHED.
func looksLikeSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// objectExists reports whether git still has the object (any type). Existence, not
// reachability, is the liveness signal: an unreachable-but-present commit may still
// back uncommitted work, whereas a missing object can back nothing.
func objectExists(repoRoot, sha string) bool {
	cmd := exec.Command("git", "-C", repoRoot, "cat-file", "-e", sha)
	return cmd.Run() == nil
}
