package gitnotes

import (
	"os/exec"
	"strings"

	"github.com/blamely/blamely/internal/authorship"
	"github.com/blamely/blamely/internal/gitutil"

	"github.com/blamely/blamely/internal/procattr"
)

// Git-op robustness for Attribution (docs/attribution-v2-design.md §8, Phase 5).
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
//

// ensureNotesFollowRewrites adds the blamely notes ref to notes.rewriteRef (local,
// idempotent) so amend/rebase carry the note onto the rewritten commit. It also
// pins notes.rewriteMode=overwrite: git's default for an N→1 squash fold is
// `concatenate`, which mashes N JSON notes into one unparsable blob — overwrite
// keeps the copied note parseable (the last pick's), and the post-rewrite hook
// then rebuilds the full merged note from all folded sources.
func ensureNotesFollowRewrites(repoPath string) {
	if !authorship.Enabled() {
		return
	}
	if out, err := procattr.Hide(exec.Command("git", "-C", repoPath, "config", "--get", "notes.rewriteMode")).Output(); err != nil ||
		strings.TrimSpace(string(out)) != "overwrite" {
		_ = procattr.Hide(exec.Command("git", "-C", repoPath, "config", "notes.rewriteMode", "overwrite")).Run()
	}
	out, err := procattr.Hide(exec.Command("git", "-C", repoPath, "config", "--get-all", "notes.rewriteRef")).Output()
	if err == nil {
		for _, l := range strings.Split(string(out), "\n") {
			if strings.TrimSpace(l) == NotesRef {
				return // already configured
			}
		}
	}
	_ = procattr.Hide(exec.Command("git", "-C", repoPath, "config", "--add", "notes.rewriteRef", NotesRef)).Run()
}

// attributionShouldSkip reports whether to skip (re)attribution because a history-
// rewriting git-op (rebase/merge/revert) is in progress: the rewritten commit
// inherits the original's note via notes.rewriteRef, so recomputing here would
// replace the correct attribution with a v1 fallback.
//
// CHERRY-PICK deliberately does NOT skip: git never copies notes for cherry-pick
// (notes.rewriteRef covers only amend/rebase), and CHERRY_PICK_HEAD is still
// present when the post-commit hook fires for a clean pick — skipping there left
// the picked commit with NO note at all. Instead the pick attributes normally and
// detectReplayOp + reconcileAddsFromSourceNotes carry the source commit's AI
// attribution onto the new SHA.
func attributionShouldSkip(repoPath string) bool {
	return authorship.Enabled() && gitutil.InProgressRewrite(repoPath)
}
