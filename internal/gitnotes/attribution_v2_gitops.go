package gitnotes

import (
	"os/exec"
	"strings"

	"github.com/blamely/blamely/internal/authorship"
	"github.com/blamely/blamely/internal/gitutil"
)

// Git-op robustness for Attribution v2 (docs/attribution-v2-design.md §8, Phase 5).
//
// A history rewrite (amend/rebase/cherry-pick) changes commit SHAs, so the working
// log — keyed by the pre-rewrite parent — no longer matches the rewritten commit's
// parent. Recomputing attribution then would fall back to v1 and CLOBBER the correct
// note. Two coordinated steps prevent that:
//
//  1. ensureNotesFollowRewrites makes git COPY the blamely notes ref across rewrites
//     (notes.rewriteRef), so a rebased commit inherits the original's flipped note.
//  2. attributionShouldSkip skips (re)attribution WHILE a rewrite is in progress, so
//     the per-commit post-commit hook doesn't overwrite the note git is about to copy.
//
// Both are gated on the v2 flag, so default (v1) behavior is unchanged.

// ensureNotesFollowRewrites adds the blamely notes ref to notes.rewriteRef (local,
// idempotent) so amend/rebase/cherry-pick carry the note onto the rewritten commit.
func ensureNotesFollowRewrites(repoPath string) {
	if !authorship.Enabled() {
		return
	}
	out, err := exec.Command("git", "-C", repoPath, "config", "--get-all", "notes.rewriteRef").Output()
	if err == nil {
		for _, l := range strings.Split(string(out), "\n") {
			if strings.TrimSpace(l) == NotesRef {
				return // already configured
			}
		}
	}
	_ = exec.Command("git", "-C", repoPath, "config", "--add", "notes.rewriteRef", NotesRef).Run()
}

// attributionShouldSkip reports whether to skip (re)attribution because a history-
// rewriting git-op (rebase/cherry-pick/merge/revert) is in progress: the rewritten
// commit inherits the original's note via notes.rewriteRef, so recomputing here
// would replace the correct attribution with a v1 fallback.
func attributionShouldSkip(repoPath string) bool {
	return authorship.Enabled() && gitutil.InProgressOp(repoPath)
}
