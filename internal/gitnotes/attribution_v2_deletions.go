package gitnotes

import "github.com/blamely/blamely/internal/authorship"

// flipDeletionsToWorkingLog attributes a commit's DELETED lines from the Attribution
// v2 deletions log (who removed which line content), instead of the legacy hash
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
