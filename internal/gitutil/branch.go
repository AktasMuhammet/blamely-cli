package gitutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// BranchName returns the short name of the currently checked-out branch for the
// repo containing `p` (e.g. "main", "feature/x"). Returns "" if HEAD is detached
// or git fails — callers treat "" as "no branch" (a detached/unknown session).
func BranchName(p string) string {
	dir := repoDir(p)
	out, err := exec.Command("git", "-C", dir, "symbolic-ref", "--quiet", "--short", "HEAD").Output()
	if err != nil {
		return "" // detached HEAD or not a repo
	}
	return strings.TrimSpace(string(out))
}

// DefaultBranch returns the repository's default branch name (the branch
// origin/HEAD points at), falling back to "main" then "master" if it can't be
// resolved. Used only to compute a session's base_sha via merge-base.
func DefaultBranch(p string) string {
	dir := repoDir(p)
	// origin/HEAD -> e.g. "origin/main"
	if out, err := exec.Command("git", "-C", dir, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD").Output(); err == nil {
		ref := strings.TrimSpace(string(out))
		if i := strings.LastIndexByte(ref, '/'); i >= 0 {
			ref = ref[i+1:]
		}
		if ref != "" {
			return ref
		}
	}
	for _, cand := range []string{"main", "master"} {
		if err := exec.Command("git", "-C", dir, "rev-parse", "--verify", "--quiet", cand).Run(); err == nil {
			return cand
		}
	}
	return "main"
}

// HeadSHA returns the current HEAD commit SHA for the repo containing `p`, or ""
// if the repo has no commits yet or git fails. This is the work-session base_sha
// while editing: one session per (branch, HEAD) until the next commit advances HEAD.
func HeadSHA(p string) string {
	dir := repoDir(p)
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ParentCommitSHA returns the parent of `commitSHA` in the repo containing `p`,
// or "" for a root commit or when git fails. Used at commit time to resolve the
// work session whose uncommitted edits are being attributed.
func ParentCommitSHA(p, commitSHA string) string {
	if commitSHA == "" {
		return ""
	}
	dir := repoDir(p)
	out, err := exec.Command("git", "-C", dir, "rev-parse", commitSHA+"^").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// MergeBase returns the merge-base SHA between the given ref and HEAD for the
// repo containing `p`, or "" if it can't be computed (e.g. ref doesn't exist or
// HEAD has no commits yet).
func MergeBase(p, ref string) string {
	if ref == "" {
		return ""
	}
	dir := repoDir(p)
	out, err := exec.Command("git", "-C", dir, "merge-base", ref, "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// InProgressOp reports whether the repo containing `p` is in the middle of a
// history-rewriting operation (cherry-pick, merge, revert, rebase). Edits the
// editor observes during these are replays of existing content, not fresh
// authorship, so detectors pause recording while one is in progress.
func InProgressOp(p string) bool {
	return anyMarker(p,
		"CHERRY_PICK_HEAD", "MERGE_HEAD", "REVERT_HEAD",
		"rebase-merge", "rebase-apply",
	)
}

// InProgressRewrite is InProgressOp WITHOUT the cherry-pick marker. Used by the
// attribution skip: rebase/amend commits inherit their note via notes.rewriteRef
// so recomputing mid-rewrite must be skipped — but git does NOT copy notes for
// cherry-pick, and CHERRY_PICK_HEAD is still present when the post-commit hook
// fires for a clean pick, so skipping there would leave the picked commit with
// no note at all. Cherry-picked commits attribute normally (the replay detection
// carries the source note's AI attribution over).
func InProgressRewrite(p string) bool {
	return anyMarker(p,
		"MERGE_HEAD", "REVERT_HEAD",
		"rebase-merge", "rebase-apply",
	)
}

func anyMarker(p string, markers ...string) bool {
	dir := repoDir(p)
	gitDir, err := exec.Command("git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-dir").Output()
	if err != nil {
		return false
	}
	g := strings.TrimSpace(string(gitDir))
	if g == "" {
		return false
	}
	for _, marker := range markers {
		if _, err := pathStat(filepath.Join(g, marker)); err == nil {
			return true
		}
	}
	return false
}

// CherryPickHead returns the SOURCE commit sha of an in-flight cherry-pick —
// the content of .git/CHERRY_PICK_HEAD — and whether the marker exists. The
// marker is still present when the post-commit hook fires for a clean
// cherry-pick (pinned by TestReplayMarkerTiming_CherryPickHeadPresentAtPostCommit),
// which is what lets commit-time attribution identify the source commit and
// carry its AI attribution over.
func CherryPickHead(p string) (sha string, ok bool) {
	dir := repoDir(p)
	gitDir, err := exec.Command("git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-dir").Output()
	if err != nil {
		return "", false
	}
	g := strings.TrimSpace(string(gitDir))
	if g == "" {
		return "", false
	}
	data, err := os.ReadFile(filepath.Join(g, "CHERRY_PICK_HEAD"))
	if err != nil {
		return "", false
	}
	s := strings.TrimSpace(string(data))
	if len(s) < 40 {
		return "", false
	}
	return s, true
}

// repoDir resolves `p` to a directory git can run in (the dir itself, or the
// parent if `p` is a file). Symlinks are not resolved here — git tolerates
// either form and the callers above only need a valid -C target.
func repoDir(p string) string {
	if fi, err := pathStat(p); err == nil && !fi.IsDir() {
		return filepath.Dir(p)
	}
	return p
}
