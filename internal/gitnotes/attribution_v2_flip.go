package gitnotes

import (
	"strings"

	"github.com/blamely/blamely/internal/authorship"
	"github.com/blamely/blamely/internal/store"
)

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

// migrateWorkingLogsOnCommit finalizes working logs after a commit: files NOT in
// this commit migrate forward to the new HEAD (so uncommitted attribution survives a
// partial commit), and files IN this commit have their working logs DELETED (their
// attribution now lives in the note + SQLite). Must run AFTER the flip has read the
// parent-base logs. Flag-gated.
func migrateWorkingLogsOnCommit(repoPath string, note *Note) {
	if note == nil || !authorship.Enabled() {
		return
	}
	parent := commitParentSHA(repoPath, note.Commit)
	if parent == "" {
		return
	}
	// Files included in THIS commit have their working logs deleted (note + SQLite
	// hold the attribution); only uncommitted files migrate forward to the new HEAD.
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
	flipAddsAtBase(repoPath, note.Branch, parent, note)
}

// flipAddsAtBase rewrites the note's added-line attribution from the working logs at
// `base` (the commit's parent for a committed note, or HEAD for the uncommitted
// working tree), then recomputes the added-line aggregates.
func flipAddsAtBase(repoPath, branch, base string, note *Note) {
	flipped := false
	for fi := range note.Files {
		fe := &note.Files[fi]
		authors, ok := authorship.AuthorsForFile(repoPath, branch, base, fe.Path)
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

// reconcileAddsFromEdits is the lossless cross-check the working-log flip can't
// do. Chat edits record their content_sha with PLACEHOLDER line positions (see
// tools.copilotAddedRangesFromContent: "positions are just stable placeholders"),
// because the apply-time line numbers drift before commit. The whole-file
// working-log fold therefore can't tell which AI-authored line is which when the
// AI emits content that duplicates lines already in the file — e.g. a new CSS
// button block byte-identical (bar its selector/colour) to a sibling block. The
// fold collapses that ambiguity to a single guess and the genuinely AI-authored
// lines fall through to Human, while the duplicate (unchanged) lines get the AI
// mark.
//
// Scoped to THIS commit's ADDED lines the match is unambiguous: unchanged lines
// aren't candidates, so they can't steal the attribution. For every added line
// the flip left as Human whose exact committed content matches a content_sha an
// AI tool recorded within this commit's window (consume-once, confidence-then-
// recency ordered — the same matcher the deletion path uses), credit it to that
// tool. Human→AI ONLY: a line the working log already attributed to an AI tool is
// never downgraded, and a content_sha the AI never recorded never flips a human
// line. The committed diff + the recorded edits are both exact artifacts, so this
// is a deterministic set operation, not a heuristic.
func reconcileAddsFromEdits(db *store.DB, repoID string, commitNanos int64, note *Note, added []AddedLine) {
	if db == nil || note == nil || !authorship.Enabled() || len(added) == 0 {
		return
	}
	// Same window as the working-log flip and deletion attribution: the lower
	// bound excludes edits a previous commit on this branch already consumed; the
	// 5s post-commit slack covers git's second-precision commit timestamp against
	// nanosecond edit timestamps.
	sinceNanos := db.PreviousCommitTimestampNanos(repoID, commitNanos)
	const sameSecondSlackNanos = int64(5 * 1e9)
	maxNanos := commitNanos + sameSecondSlackNanos

	// Committed content of each added line, keyed by file then line number.
	contentByLine := map[string]map[int]string{}
	for _, a := range added {
		if contentByLine[a.File] == nil {
			contentByLine[a.File] = map[int]string{}
		}
		contentByLine[a.File][a.LineNum] = a.Content
	}

	changedAny := false
	for fi := range note.Files {
		fe := &note.Files[fi]
		lineContent := contentByLine[fe.Path]
		if len(lineContent) == 0 {
			continue
		}
		// AI edits for this file in-window, each with a consume-once budget of the
		// exact and whitespace-normalized content_shas it recorded as ADDED lines.
		// A second matcher tags copy-pasted lines: author_type stays Human, but the
		// distinct copypaste tool is preserved (matching v1's by_tool nuance).
		m := newEditMatcher(db, repoID, fe.Path, sinceNanos, maxNanos, aiEligible)
		cp := newEditMatcher(db, repoID, fe.Path, sinceNanos, maxNanos, copyPasteEligible)
		if m.empty() && cp.empty() {
			continue
		}

		// Expand add ranges to single lines, upgrade Human→AI where content matches
		// a recorded AI edit (or tag copy-paste), then re-collapse (mirrors
		// flipAddsAtBase). Deletion ranges pass through untouched.
		var rewritten []RangeEntry
		changed := false
		for _, r := range fe.Lines {
			if r.Type != "add" {
				rewritten = append(rewritten, r)
				continue
			}
			for ln := r.Start; ln <= r.End; ln++ {
				single := RangeEntry{Start: ln, End: ln, Type: "add", AuthorType: r.AuthorType, Tool: r.Tool, Model: r.Model, GenType: r.GenType}
				if single.AuthorType != "AI" {
					if e := m.match(lineContent[ln]); e != nil {
						single = aiAddRange(ln, e)
						changed = true
					} else if e := cp.match(lineContent[ln]); e != nil {
						// Copy-paste: keep author_type Human, tag the tool.
						single.Tool = string(e.Tool)
						changed = true
					}
				}
				rewritten = append(rewritten, single)
			}
		}
		if changed {
			// Blank separator lines the AI inserted inside its block (e.g. between
			// two CSS rules) carry no content_sha, so pickAddEdit can't match them.
			// buildNote's inheritBlankLineAttribution would have caught them, but it
			// ran BEFORE this reconcile when the whole block was still Human. Re-run
			// the same backward-then-forward inheritance now that the surrounding
			// lines are AI.
			inheritBlankAddRanges(rewritten, lineContent)
			fe.Lines = collapseAddRanges(rewritten)
			changedAny = true
		}
	}
	if changedAny {
		recomputeAddedAggregates(note)
	}
}

// inheritBlankAddRanges gives each blank Human added line the AI attribution of
// its nearest non-blank added neighbour (backward first, then forward), matching
// buildNote's inheritBlankLineAttribution. Operates on the expanded single-line
// add entries in place; deletion entries are skipped as neighbours. A blank whose
// nearest non-blank add neighbour is Human stays Human.
//
// The neighbour search walks CONTIGUOUS added line numbers only (ln±1, ln±2, …)
// and stops at the first gap — an unchanged line, or a deletion. A blank added
// line isolated by unchanged content therefore never inherits a distant AI block's
// attribution (repro: commit 095f6221 — a lone blank line at 4 wrongly inherited
// `completion` from an AI add 9 rows away at line 13).
func inheritBlankAddRanges(entries []RangeEntry, lineContent map[int]string) {
	// Index add entries by their (post-image) line number.
	addAt := make(map[int]int, len(entries)) // line → index in entries
	for i := range entries {
		if entries[i].Type == "add" {
			addAt[entries[i].Start] = i
		}
	}
	blank := func(ln int) bool { return strings.TrimSpace(lineContent[ln]) == "" }
	// nearestNonBlankAdd walks contiguous added lines outward from ln (backward
	// first, then forward), returning the first non-blank added entry it reaches
	// before a gap. Backward wins: a blank inherits the block above it, matching
	// inheritBlankLineAttribution.
	nearestNonBlankAdd := func(ln int) (RangeEntry, bool) {
		for k := ln - 1; ; k-- {
			i, ok := addAt[k]
			if !ok {
				break // gap → try forward
			}
			if !blank(k) {
				return entries[i], true
			}
		}
		for k := ln + 1; ; k++ {
			i, ok := addAt[k]
			if !ok {
				break
			}
			if !blank(k) {
				return entries[i], true
			}
		}
		return RangeEntry{}, false
	}
	for i := range entries {
		if entries[i].Type != "add" || !blank(entries[i].Start) || entries[i].AuthorType == "AI" {
			continue
		}
		src, ok := nearestNonBlankAdd(entries[i].Start)
		if !ok || src.AuthorType != "AI" {
			continue
		}
		entries[i].AuthorType = "AI"
		entries[i].Tool = src.Tool
		entries[i].Model = src.Model
		entries[i].GenType = src.GenType
	}
}

// pickAddEdit returns the AI edit (the slice is confidence-then-recency ordered,
// so the strongest/newest record wins) that recorded `content` and still has
// unconsumed budget for it, consuming one unit. Exact content_sha is preferred;
// the whitespace-normalized hash is a fallback for autoformatter drift (an
// AI-written line whose exact bytes were later reindented). A recorded line is
// ONE unit of budget shared across both hashes — when an exact match is consumed
// its normalized counterpart is decremented too — so content the AI recorded once
// can attribute at most one committed line. That strictness is deliberate:
// over-crediting a human duplicate to AI is the same class of wrong detection,
// just inverted. Blank/whitespace-only content never carries a content_sha (see
// tools.copilotAddedRangesFromContent) and never matches.
func pickAddEdit(edits []store.Edit, content string, remSHA, remNorm map[int64]map[string]int) *store.Edit {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	want := sha256HexStr([]byte(content))
	for i := range edits {
		e := &edits[i]
		if remSHA[e.ID][want] > 0 {
			remSHA[e.ID][want]--
			if wn := sha256HexNormStr(content); wn != "" && remNorm[e.ID][wn] > 0 {
				remNorm[e.ID][wn]-- // keep the shared per-line budget in sync
			}
			return e
		}
	}
	wantNorm := sha256HexNormStr(content)
	if wantNorm == "" {
		return nil
	}
	for i := range edits {
		e := &edits[i]
		if remNorm[e.ID][wantNorm] > 0 {
			remNorm[e.ID][wantNorm]--
			return e
		}
	}
	return nil
}

// aiAddRange builds an AI-authored added RangeEntry for one line from the edit
// that recorded its content (mirrors rangeForWorkingLogLine's AI branch, but
// sourced from a store.Edit instead of an authorship.Author).
func aiAddRange(ln int, e *store.Edit) RangeEntry {
	re := RangeEntry{Start: ln, End: ln, Type: "add", AuthorType: "AI", Tool: string(e.Tool)}
	if e.Model.Valid && e.Model.String != "" {
		m := e.Model.String
		re.Model = &m
	}
	if gt := string(e.GenType); gt != "" && gt != string(store.GenTypeUnknown) {
		re.GenType = &gt
	}
	return re
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
				// A Human line may still carry a non-AI tool tag (copypaste): keep it
				// in by_tool for the nuance, without affecting the AI/Human split.
				if r.Tool != "" {
					toolLines[r.Tool] += n
				}
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
