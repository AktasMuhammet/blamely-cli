package gitnotes

import "github.com/blamely/blamely/internal/authorship"

// Phase 3 flip (docs/attribution-v2-design.md §10): when BLAMELY_ATTRIBUTION_V2 is
// on, rewrite the note's ADDED-line attribution from the Attribution v2 working log
// (the diff-based truth) instead of the content-hash matcher, and recompute the
// AI/Human totals, by_gen_type, and by_tool line counts to match. Deleted-line
// attribution stays v1 (the working log doesn't track deletions yet — Phase 4); a
// file with no working log keeps its v1 attribution (degraded, not wrong). Behind
// the flag, so default behavior is unchanged.

// gcWorkingLogsIfEnabled prunes dangling-base working logs after a commit
// (flag-gated, best-effort) so history-rewrite churn doesn't accumulate on disk.
func gcWorkingLogsIfEnabled(repoPath string) {
	if !authorship.Enabled() {
		return
	}
	_, _ = authorship.GCWorkingLogs(repoPath)
}

// migrateWorkingLogsOnCommit re-keys the just-committed parent's working logs onto
// the new HEAD (this commit), so uncommitted attribution for files NOT in this commit
// survives a partial commit, and tracked logs follow HEAD instead of stranding at the
// parent base. Must run AFTER the flip has read the parent-base logs. Flag-gated.
func migrateWorkingLogsOnCommit(repoPath string, note *Note) {
	if note == nil || !authorship.Enabled() {
		return
	}
	parent := commitParentSHA(repoPath, note.Commit)
	if parent == "" {
		return
	}
	// Files included in THIS commit stay at the parent base (amend/rebase re-flips
	// them from there); only uncommitted files migrate forward to the new HEAD.
	committed := make(map[string]bool, len(note.Files))
	for _, f := range note.Files {
		committed[f.Path] = true
	}
	_ = authorship.MigrateWorkingLogs(repoPath, note.Branch, parent, note.Commit, committed)
}

func flipNoteToWorkingLog(repoPath string, note *Note) {
	if note == nil || !authorship.Enabled() {
		return
	}
	parent := commitParentSHA(repoPath, note.Commit)
	if parent == "" {
		return
	}
	flipped := false
	for fi := range note.Files {
		fe := &note.Files[fi]
		authors, ok := authorship.AuthorsForFile(repoPath, note.Branch, parent, fe.Path)
		if !ok {
			continue // not tracked by v2 → keep this file's v1 attribution
		}
		var rewritten []RangeEntry
		for _, r := range fe.Lines {
			if r.Type != "add" {
				rewritten = append(rewritten, r) // deletions stay v1
				continue
			}
			for ln := r.Start; ln <= r.End; ln++ {
				rewritten = append(rewritten, rangeForWorkingLogLine(ln, authors[ln]))
			}
		}
		fe.Lines = collapseAddRanges(rewritten)
		flipped = true
	}
	if flipped {
		recomputeAddedAggregates(note)
	}
}

func rangeForWorkingLogLine(ln int, a authorship.Author) RangeEntry {
	re := RangeEntry{Start: ln, End: ln, Type: "add", AuthorType: "Human"}
	if a.Type == authorship.AI {
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
	return re
}

// collapseAddRanges merges adjacent single-line entries that share the full
// (type, author_type, tool, model, gen_type) identity back into spans.
func collapseAddRanges(in []RangeEntry) []RangeEntry {
	var out []RangeEntry
	for _, r := range in {
		if n := len(out); n > 0 && out[n-1].End == r.Start-1 && sameRangeIdentity(out[n-1], r) {
			out[n-1].End = r.End
			continue
		}
		out = append(out, r)
	}
	return out
}

func sameRangeIdentity(a, b RangeEntry) bool {
	return a.Type == b.Type && a.AuthorType == b.AuthorType && a.Tool == b.Tool &&
		ptrStrEq(a.Model, b.Model) && ptrStrEq(a.GenType, b.GenType)
}

func ptrStrEq(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// recomputeAddedAggregates rebuilds the ADDED-line split (Totals, by_gen_type, and
// by_tool line counts) from the now-working-log-sourced ranges. Deleted totals and
// per-tool tokens/model/suggested are preserved from v1.
func recomputeAddedAggregates(note *Note) {
	aiAdded, humanAdded := 0, 0
	var gt ByGenType
	toolLines := map[string]int{}
	models := map[string]int{}

	for _, fe := range note.Files {
		for _, r := range fe.Lines {
			if r.Type != "add" {
				continue
			}
			n := r.End - r.Start + 1
			if n <= 0 {
				continue
			}
			if r.AuthorType == "AI" {
				aiAdded += n
				if r.Tool != "" {
					toolLines[r.Tool] += n
				}
				if r.Model != nil && *r.Model != "" {
					models[*r.Model] += n
				}
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
				humanAdded += n
				gt.Human += n
			}
		}
	}

	note.Totals.AILines = aiAdded
	note.Totals.HumanLines = humanAdded
	note.Totals.AddedLines = aiAdded + humanAdded
	if len(models) > 0 {
		note.Totals.Models = models
	} else {
		note.Totals.Models = nil
	}
	note.ByGenType = gt

	// Rebuild by_tool line counts, preserving each tool's v1 metadata
	// (tokens/model/suggested/deleted) where present.
	rebuilt := make(map[string]Tool, len(toolLines))
	for tool, lines := range toolLines {
		t := note.ByTool[tool] // zero value if the tool is new
		t.Lines = lines
		t.AcceptedLines = lines
		rebuilt[tool] = t
	}
	// Keep deletion-only tools (authored nothing but removed lines).
	for tool, t := range note.ByTool {
		if _, ok := rebuilt[tool]; !ok && t.DeletedLines > 0 {
			t.Lines = 0
			t.AcceptedLines = 0
			rebuilt[tool] = t
		}
	}
	note.ByTool = rebuilt
}

func ptrStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
