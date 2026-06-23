package gitnotes

import (
	"github.com/blamely/blamely/internal/authorship"
	"github.com/blamely/blamely/internal/store"
)

// flipDeletionsToWorkingLog attributes a commit's DELETED lines from the
// deletions log (who removed which line content), instead of the legacy hash
// matcher. It matches each note delete-range line to its content (from the diff) and
// then to the recorded deleter, rewrites the delete ranges, and recomputes the
// deleted totals + per-tool deleted counts. Flag-gated; a line with no recorded
// deletion stays Human (the legacy default). Editor-originated deletions aren't
// recorded yet and fall through unchanged.
func flipDeletionsToWorkingLog(repoPath string, note *Note, change *CommitChange) {
	if note == nil || change == nil || !authorship.Enabled() {
		return
	}
	parent := commitParentSHA(repoPath, note.Commit)
	if parent == "" {
		return
	}
	flipDeletesAtBase(repoPath, note.Branch, parent, note, change)
}

// flipDeletesAtBase rewrites the note's deleted-line attribution from the deletions log
// at `base` (the commit's parent, or HEAD for the uncommitted working tree).
func flipDeletesAtBase(repoPath, branch, base string, note *Note, change *CommitChange) {
	deletions, err := authorship.LoadDeletions(repoPath, branch, base)
	if err != nil || len(deletions) == 0 {
		return
	}
	changed := false
	for fi := range note.Files {
		fe := &note.Files[fi]
		// change.Deleted is keyed by the PRE-commit path (= the rename source, if any).
		prePath := fe.Path
		if orig, ok := change.Renames[fe.Path]; ok {
			prePath = orig
		}
		delLines := change.Deleted[prePath]
		if len(delLines) == 0 {
			continue
		}
		byLineContent := make(map[int]string, len(delLines))
		for _, d := range delLines {
			byLineContent[d.LineNum] = d.Content
		}
		fileDeletions := deletions[prePath]
		if fileDeletions == nil {
			fileDeletions = deletions[fe.Path]
		}
		if fileDeletions == nil {
			continue
		}

		var rewritten []RangeEntry
		for _, r := range fe.Lines {
			if r.Type != "delete" {
				rewritten = append(rewritten, r)
				continue
			}
			for ln := r.Start; ln <= r.End; ln++ {
				re := RangeEntry{Start: ln, End: ln, Type: "delete", AuthorType: "Human"}
				if content, ok := byLineContent[ln]; ok {
					if a, ok2 := fileDeletions[content]; ok2 && a.Type == authorship.AI {
						re.AuthorType = "AI"
						re.Tool = a.Tool
						if a.Model != "" {
							m := a.Model
							re.Model = &m
						}
						if a.GenType != "" {
							g := a.GenType
							re.GenType = &g
						}
					}
				}
				rewritten = append(rewritten, re)
			}
		}
		fe.Lines = collapseAddRanges(rewritten) // collapse is type-aware; safe for deletes
		changed = true
	}
	if changed {
		recomputeDeletedAggregates(note)
	}
}

// reconcileDeletesFromEdits is the deletion twin of reconcileAddsFromEdits — and
// the lossless cross-check flipDeletionsToWorkingLog can't do. The deletion flip
// reads ONLY the editor-tracker deletions log (authorship.LoadDeletions); a chat
// tool that removed lines (copilot/claude chat, codex cli) records its removed-line
// hashes in SQLite (edit_removed_lines) but writes no deletions-log entry, so those
// deletions fall through to Human. Here, for every committed deleted line the flip
// left Human, match its content against a removed-line content_sha an AI tool
// recorded within this commit's window (consume-once, exact-then-norm, via the same
// pickEditForRemovedLine the legacy matcher uses) and credit it to that tool.
// Human→AI only; never downgrades a deletion the deletions log already attributed.
func reconcileDeletesFromEdits(db *store.DB, repoID string, commitNanos int64, note *Note, change *CommitChange) {
	if db == nil || note == nil || change == nil || !authorship.Enabled() {
		return
	}
	// Same window as flipDeletionsToWorkingLog / the add reconciliation.
	sinceNanos := db.PreviousCommitTimestampNanos(repoID, commitNanos)
	const sameSecondSlackNanos = int64(5 * 1e9)
	maxNanos := commitNanos + sameSecondSlackNanos

	changedAny := false
	for fi := range note.Files {
		fe := &note.Files[fi]
		// change.Deleted is keyed by the PRE-commit path (rename source, if any).
		prePath := fe.Path
		if orig, ok := change.Renames[fe.Path]; ok {
			prePath = orig
		}
		delLines := change.Deleted[prePath]
		if len(delLines) == 0 {
			continue
		}
		byLineContent := make(map[int]string, len(delLines))
		for _, d := range delLines {
			byLineContent[d.LineNum] = d.Content
		}

		// AI edits that recorded REMOVED lines for this file, in window.
		edits, err := db.EditsForFileSince(repoID, prePath, sinceNanos)
		if err != nil {
			continue
		}
		aiEdits := make([]store.Edit, 0, len(edits))
		for i := range edits {
			e := &edits[i]
			if e.TimestampNanos <= sinceNanos || e.TimestampNanos > maxNanos {
				continue
			}
			if authorTypeFor(e.Tool) != "AI" || len(e.RemovedLines) == 0 || editFromWholeFileWrite(e) {
				continue
			}
			aiEdits = append(aiEdits, *e)
		}
		if len(aiEdits) == 0 {
			continue
		}
		remSHA, remNorm := removedHashMultisets(aiEdits)

		var rewritten []RangeEntry
		changed := false
		for _, r := range fe.Lines {
			if r.Type != "delete" {
				rewritten = append(rewritten, r)
				continue
			}
			for ln := r.Start; ln <= r.End; ln++ {
				re := RangeEntry{Start: ln, End: ln, Type: "delete", AuthorType: r.AuthorType, Tool: r.Tool, Model: r.Model, GenType: r.GenType}
				if re.AuthorType != "AI" {
					if e := pickEditForRemovedLine(aiEdits, byLineContent[ln], sinceNanos, maxNanos, remSHA, remNorm); e != nil {
						re = aiDeleteRange(ln, e)
						changed = true
					}
				}
				rewritten = append(rewritten, re)
			}
		}
		if changed {
			fe.Lines = collapseAddRanges(rewritten) // type-aware; safe for deletes
			changedAny = true
		}
	}
	if changedAny {
		recomputeDeletedAggregates(note)
	}
}

// aiDeleteRange builds an AI-authored deleted RangeEntry for one line from the edit
// that recorded its removal (the deletion counterpart of aiAddRange).
func aiDeleteRange(ln int, e *store.Edit) RangeEntry {
	re := RangeEntry{Start: ln, End: ln, Type: "delete", AuthorType: "AI", Tool: string(e.Tool)}
	if e.Model.Valid && e.Model.String != "" {
		m := e.Model.String
		re.Model = &m
	}
	if gt := string(e.GenType); gt != "" && gt != string(store.GenTypeUnknown) {
		re.GenType = &gt
	}
	return re
}

// recomputeByGenType rebuilds by_gen_type FROM SCRATCH off the final per-line ranges —
// every added AND deleted line, by its author/gen_type — so the generation breakdown
// (and the bars rendered from it) covers all changed lines.
//
// It resets and recomputes rather than incrementally adding because several earlier
// steps touch by_gen_type (buildNote's attribution, flipFileToAI's Human→AI moves,
// recomputeAddedAggregates' adds-only rebuild). Those overlap inconsistently — for a
// PURE-DELETION commit recomputeAddedAggregates never runs, so an incremental "add the
// deletions" step double-counted them (a 12-line deletion showed as 24). Deriving the
// whole breakdown from the settled ranges is idempotent and can't double-count. MUST
// be the FINAL by_gen_type step, after both add-recompute and deletion attribution.
func recomputeByGenType(note *Note) {
	if note == nil {
		return
	}
	var gt ByGenType
	for _, fe := range note.Files {
		for _, r := range fe.Lines {
			if r.Type != "add" && r.Type != "delete" {
				continue
			}
			n := r.End - r.Start + 1
			if n <= 0 {
				continue
			}
			if r.AuthorType == "AI" {
				switch ptrStr(r.GenType) {
				case "chat":
					gt.Chat += n
				case "cli":
					gt.CLI += n
				case "completion":
					gt.Completion += n
				default:
					gt.Unknown += n
				}
			} else {
				gt.Human += n
			}
		}
	}
	note.ByGenType = gt
}

// recomputeDeletedAggregates rebuilds the deleted-line totals + per-tool deleted
// counts from the (now working-log-sourced) delete ranges.
func recomputeDeletedAggregates(note *Note) {
	aiDel, humanDel := 0, 0
	toolDel := map[string]int{}
	for _, fe := range note.Files {
		for _, r := range fe.Lines {
			if r.Type != "delete" {
				continue
			}
			n := r.End - r.Start + 1
			if n <= 0 {
				continue
			}
			if r.AuthorType == "AI" {
				aiDel += n
				if r.Tool != "" {
					toolDel[r.Tool] += n
				}
			} else {
				humanDel += n
			}
		}
	}
	note.Totals.AIDeletedLines = aiDel
	note.Totals.HumanDeletedLines = humanDel
	note.Totals.DeletedLines = aiDel + humanDel
	if note.ByTool == nil {
		note.ByTool = map[string]Tool{}
	}
	// Reset deleted counts, then set from the recompute (a tool may delete without adding).
	for tool, t := range note.ByTool {
		if t.DeletedLines != 0 {
			t.DeletedLines = 0
			note.ByTool[tool] = t
		}
	}
	for tool, n := range toolDel {
		t := note.ByTool[tool]
		t.DeletedLines = n
		note.ByTool[tool] = t
	}
}
