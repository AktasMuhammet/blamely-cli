package gitnotes

// Post-rewrite handling (git's `post-rewrite` hook): git invokes the hook after
// `commit --amend` and `rebase` with stdin lines `old-sha SP new-sha` —
// INCLUDING the N→1 mappings of an interactive-rebase squash/fixup. That
// mapping is exactly what the note pipeline can't see on its own:
//
//   • 1→1 rewrites (plain pick, amend) already work — notes.rewriteRef copies
//     the note, and the amend path recomputes with inherited-note recovery.
//   • N→1 (squash/fixup) is where attribution was lost: git's default
//     notes.rewriteMode=concatenate mashes N JSON notes into one unparsable
//     blob, discarding the folded siblings' attribution.
//
// HandlePostRewrite rebuilds a single valid note for every N→1 target from the
// SOURCE commits' still-readable notes (their objects survive the rewrite),
// through the same replay pipeline a cherry-pick/squash-merge uses.

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
)

// RewritePair is one stdin line of the post-rewrite hook: old → new.
type RewritePair struct {
	Old string
	New string
}

// ParseRewritePairs reads the hook's stdin ("old-sha new-sha" per line, extra
// fields ignored). Malformed lines are skipped — the hook must never fail.
func ParseRewritePairs(r io.Reader) []RewritePair {
	var out []RewritePair
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 || len(f[0]) < 40 || len(f[1]) < 40 {
			continue
		}
		out = append(out, RewritePair{Old: f[0], New: f[1]})
	}
	return out
}

// HandlePostRewrite processes the rewrite mapping. Groups the pairs by NEW sha:
//
//   - group of 1 with a parseable note on the new sha → leave it (the
//     rewriteRef copy / amend recompute already handled it);
//   - group of 1 with NO parseable note (source was never noted, or
//     rewriteMode left a blob) and every N→1 group → rebuild via the replay
//     pipeline with the old shas as sources.
//
// Best-effort by design: errors are swallowed per group so a broken rebuild
// never breaks the user's rebase (the hook always exits 0).
func HandlePostRewrite(repoPath, kind string, pairs []RewritePair) {
	_ = kind // amend and rebase take the same rebuild path; kept for logging/evolution
	if len(pairs) == 0 {
		return
	}
	byNew := map[string][]string{}
	var order []string
	for _, p := range pairs {
		if _, seen := byNew[p.New]; !seen {
			order = append(order, p.New)
		}
		byNew[p.New] = append(byNew[p.New], p.Old)
	}
	for _, newSHA := range order {
		olds := byNew[newSHA]
		if len(olds) == 1 && loadNoteForSeed(repoPath, newSHA) != nil {
			continue // clean 1→1 rewrite with a valid copied note
		}
		func() {
			defer func() { _ = recover() }() // never break the rebase
			RebuildNoteFromSources(repoPath, newSHA, olds)
		}()
	}
}

// RebuildNoteFromSources recomputes newSHA's note through the full pipeline
// with an explicit replay context (sources = the folded commits), then merges
// the sources' token usage — the rebuilt note's applyEditTokens sees only
// in-window edits, which a squash's old edits are not.
func RebuildNoteFromSources(repoPath, newSHA string, oldSHAs []string) {
	note, err := attributeAndWriteEx(repoPath, newSHA, &replayCtx{Kind: replayRebaseSquash, SourceSHAs: oldSHAs})
	if err != nil || note == nil {
		return
	}
	if mergeSourceNoteTokens(repoPath, note, oldSHAs) {
		if body, err := json.Marshal(note); err == nil {
			_ = writeNote(repoPath, newSHA, body)
		}
	}
}

// mergeSourceNoteTokens folds the source notes' per-tool token usage into the
// rebuilt note (summed), for tools the rebuilt note attributes lines to.
// Returns true when anything changed.
func mergeSourceNoteTokens(repoPath string, note *Note, oldSHAs []string) bool {
	if note == nil || len(note.ByTool) == 0 {
		return false
	}
	changed := false
	for _, src := range oldSHAs {
		srcNote := loadNoteForSeed(repoPath, src)
		if srcNote == nil {
			continue
		}
		for tool, st := range srcNote.ByTool {
			if st.Tokens == nil {
				continue
			}
			dst, ok := note.ByTool[tool]
			if !ok {
				continue // only enrich tools the rebuilt note credits
			}
			if dst.Tokens == nil {
				dst.Tokens = &Tokens{}
			}
			dst.Tokens.Input += st.Tokens.Input
			dst.Tokens.Output += st.Tokens.Output
			dst.Tokens.CacheRead += st.Tokens.CacheRead
			dst.Tokens.CacheWrite += st.Tokens.CacheWrite
			note.ByTool[tool] = dst
			changed = true
		}
	}
	return changed
}
