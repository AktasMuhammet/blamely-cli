package gitnotes

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/blamely/blamely/internal/authorship"
	"github.com/blamely/blamely/internal/gitutil"
	"github.com/blamely/blamely/internal/store"
)

// editFromWholeFileWrite reports whether an edit came from a whole-file Write/create
// tool, whose per-line content is narrowed against the daemon's CACHED FILE SNAPSHOT
// (see tools.ResolveWholeFileWrite) rather than the AI's own diff. When that snapshot
// is stale — e.g. the human typed lines during the chat turn after the last recorded
// edit — the Write re-emits those HUMAN lines as if the AI authored them. Its
// content_shas are therefore NOT reliable evidence of AI authorship, so reconciliation
// excludes it; the keystroke-level working-log fold keeps those lines Human correctly.
// Focused edits stay eligible: chat apply_patch (copilot/cursor transcripts), claude
// Edit/MultiEdit (old/new-string narrowing), and inline completions all record the
// AI's actual change. (raw_meta's "tool" is the operation name for transcript-op
// recorders; "Write"/"create_file" never collide with the AI-name form like "claude".)
func editFromWholeFileWrite(e *store.Edit) bool {
	if e == nil || !e.RawMeta.Valid || e.RawMeta.String == "" {
		return false
	}
	var meta struct {
		Tool string `json:"tool"`
	}
	if json.Unmarshal([]byte(e.RawMeta.String), &meta) != nil {
		return false
	}
	switch strings.ToLower(meta.Tool) {
	case "write", "create_file", "createfile", "create_new_file":
		return true
	}
	return false
}

// editFromCursorTabLog reports whether an edit was recorded by the Cursor Tab
// completion-log watcher (tools.tailCursorTabLog stamps raw_meta.source =
// "cursor_tab_log"). Such an edit is AUTHORITATIVE that its lines were an
// accepted Cursor Tab (inline) suggestion — Cursor's own log, not a heuristic.
// The editor plugin, seeing the same insert first, often mis-records it as a
// clipboard paste (its content coincidentally matches the clipboard during
// repetitive edits), so the add reconcile lets a Tab-log edit override that
// copypaste record for the same content.
func editFromCursorTabLog(e *store.Edit) bool {
	if e == nil || e.Tool != store.ToolCursor || !e.RawMeta.Valid || e.RawMeta.String == "" {
		return false
	}
	var meta struct {
		Source string `json:"source"`
	}
	if json.Unmarshal([]byte(e.RawMeta.String), &meta) != nil {
		return false
	}
	return meta.Source == "cursor_tab_log"
}

// cursorTabEligible selects Cursor Tab-log edits for the add reconcile's
// highest-precedence matcher (it runs before the copy-paste matcher).
func cursorTabEligible(e *store.Edit) bool {
	return editFromCursorTabLog(e)
}

// editMatcher resolves a committed/current line's content to the AI edit that
// authored it, by content_sha. It is the SINGLE matcher shared by the commit-note
// reconciliation (reconcileAddsFromEdits) and the live-gutter reconciliation
// (ReconcileGutterOverrides), so the two can never attribute the same line
// differently — Attribution invariant I4 (note and gutter agree). It holds a
// per-edit consume-once budget of the exact and whitespace-normalized content_shas
// each in-window AI edit recorded as added lines.
type editMatcher struct {
	edits   []store.Edit
	remSHA  map[int64]map[string]int
	remNorm map[int64]map[string]int
}

// aiEligible selects AI edits whose per-line content is reliable authorship
// evidence: an AI tool, and NOT a snapshot-narrowed whole-file Write.
//
// Cursor is the exception. Its agent (composer) applies every edit through a
// "Write" tool call, so an agent apply is ALWAYS recorded as a whole-file write.
// Unlike Claude/Copilot, Cursor's editor plugin can't distinguish an agent apply
// from a human paste on a multi-AI host — no chat-apply command fires — so it
// mis-records those ADDED lines as Human in the working log and never corrects
// them. That leaves the PostToolUse-hook content_shas (already narrowed to the
// genuinely-new lines by ResolveWholeFileWrite) as the only reliable evidence,
// so we trust them here for Cursor. The add reconcile stays safe: it is Human→AI
// only, requires an exact content_sha match, runs the copy-paste matcher first
// (so a genuine human paste still wins), and is consume-once + commit-scoped.
// Claude/Copilot keep the whole-file-Write exclusion (stale-snapshot guard).
func aiEligible(e *store.Edit) bool {
	if authorTypeFor(e.Tool) != "AI" {
		return false
	}
	if e.Tool == store.ToolCursor {
		return true
	}
	return !editFromWholeFileWrite(e)
}

// copyPasteEligible selects copy-paste edits. These roll up to author_type Human
// (the clipboard source is unprovable), but the distinct tool tag is preserved.
func copyPasteEligible(e *store.Edit) bool {
	return e.Tool == store.ToolCopyPaste
}

// newEditMatcher loads the edits for repo/file recorded within (sinceNanos,
// maxNanos] that satisfy `eligible`, building their consume-once content budgets.
// Edits that recorded no content_sha are skipped. The slice keeps
// EditsForFileSince's confidence-then-recency order, so the strongest/newest
// record claims a line first.
func newEditMatcher(db *store.DB, repoID, file string, sinceNanos, maxNanos int64, eligible func(*store.Edit) bool) *editMatcher {
	m := &editMatcher{remSHA: map[int64]map[string]int{}, remNorm: map[int64]map[string]int{}}
	if db == nil {
		return m
	}
	edits, err := db.EditsForFileSince(repoID, file, sinceNanos)
	if err != nil {
		return m
	}
	for i := range edits {
		e := &edits[i]
		if e.TimestampNanos <= sinceNanos || e.TimestampNanos > maxNanos {
			continue
		}
		if !eligible(e) {
			continue
		}
		sha, norm := map[string]int{}, map[string]int{}
		for _, l := range e.Lines {
			if l.ContentSHA != "" {
				sha[l.ContentSHA]++
			}
			if l.ContentSHANorm != "" {
				norm[l.ContentSHANorm]++
			}
		}
		if len(sha) == 0 && len(norm) == 0 {
			continue
		}
		m.edits = append(m.edits, *e)
		m.remSHA[e.ID] = sha
		m.remNorm[e.ID] = norm
	}
	return m
}

func (m *editMatcher) empty() bool { return len(m.edits) == 0 }

// match returns the AI edit that recorded content and still has budget, consuming
// one unit (see pickAddEdit). Returns nil for blank content or no match.
func (m *editMatcher) match(content string) *store.Edit {
	return pickAddEdit(m.edits, content, m.remSHA, m.remNorm)
}

// editToAuthor maps a recorded AI edit to the gutter's authorship.Author shape.
func editToAuthor(e *store.Edit) authorship.Author {
	a := authorship.Author{Type: authorship.AI, Tool: string(e.Tool)}
	if e.Model.Valid && e.Model.String != "" {
		a.Model = e.Model.String
	}
	if gt := string(e.GenType); gt != "" && gt != string(store.GenTypeUnknown) {
		a.GenType = gt
	}
	return a
}

// ReconcileGutterOverrides is the live-gutter twin of reconcileAddsFromEdits. It
// returns the AI authors to apply to the uncommitted added lines (`changed`, from
// `git diff HEAD`) of wl that the whole-file working-log fold left Human, but whose
// CURRENT on-disk content matches a content_sha an AI tool recorded since baseSHA
// (HEAD) was committed. Keyed by line number; only Human→AI upgrades appear, so a
// line the working log already attributed to an AI tool is never touched. Blank
// separator lines inside the changed set inherit a matched/already-AI neighbour,
// mirroring inheritBlankAddRanges. Scoped to `changed` so unchanged duplicate
// content can't be mis-attributed — the same guard the note path uses — and it
// shares editMatcher/pickAddEdit with the note so gutter and note agree (I4).
//
// Returns nil when there is nothing to add (attribution off, no changes, file unreadable,
// or no in-window AI edits), so callers can skip cheaply.
func ReconcileGutterOverrides(db *store.DB, repoRoot, baseSHA string, wl *authorship.WorkingLog, changed map[int]bool) map[int]authorship.Author {
	if db == nil || wl == nil || len(changed) == 0 || !authorship.Enabled() {
		return nil
	}
	repoID, _ := gitutil.RepoID(repoRoot)
	if repoID == "" {
		repoID = repoRoot
	}
	// Window: edits recorded AFTER HEAD was committed are the uncommitted ones the
	// gutter shows; no upper bound — every such edit up to now is a candidate.
	sinceNanos, _ := CommitTimestampNanos(repoRoot, baseSHA)
	return ReconcileGutterOverridesAt(db, repoRoot, repoID, sinceNanos, wl, changed)
}

// ReconcileGutterOverridesAt is ReconcileGutterOverrides with the two per-request
// CONSTANTS — the repo id and the base-SHA commit timestamp — passed in instead of
// recomputed. The repo-wide `--all` path resolves them ONCE and reuses them for every
// file, avoiding two `git` subprocess spawns per working log (the dominant cost on
// Windows, where process creation is expensive).
func ReconcileGutterOverridesAt(db *store.DB, repoRoot, repoID string, sinceNanos int64, wl *authorship.WorkingLog, changed map[int]bool) map[int]authorship.Author {
	if db == nil || wl == nil || len(changed) == 0 || !authorship.Enabled() {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(wl.File)))
	if err != nil {
		return nil
	}
	fileLines := strings.Split(string(data), "\n")
	lineText := func(ln int) string {
		if ln < 1 || ln > len(fileLines) {
			return ""
		}
		return strings.TrimRight(fileLines[ln-1], "\r")
	}

	m := newEditMatcher(db, repoID, wl.File, sinceNanos, math.MaxInt64, aiEligible)
	if m.empty() {
		return nil
	}
	// A copy-paste matcher guards against re-claiming a human paste as AI: a line the
	// human explicitly pasted stays Human even when its content matches an AI
	// content_sha (copying AI output is common). Mirrors reconcileAddsFromEdits so the
	// gutter and note agree (I4). Only built when there are AI edits to upgrade against.
	cp := newEditMatcher(db, repoID, wl.File, sinceNanos, math.MaxInt64, copyPasteEligible)

	// Lines the fold already attributed to AI keep that attribution (and don't
	// spend match budget); every other changed line is a Human upgrade candidate.
	alreadyAI := map[int]bool{}
	for _, r := range wl.Lines {
		if r.Author.Type == authorship.AI {
			for ln := r.Start; ln <= r.End; ln++ {
				alreadyAI[ln] = true
			}
		}
	}

	changedSorted := make([]int, 0, len(changed))
	for ln := range changed {
		changedSorted = append(changedSorted, ln)
	}
	sort.Ints(changedSorted)

	overrides := map[int]authorship.Author{}
	for _, ln := range changedSorted {
		if alreadyAI[ln] {
			continue
		}
		// Copy-paste first: an explicitly-pasted line keeps its Human (fold)
		// attribution rather than being upgraded to the AI whose code was copied.
		if e := cp.match(lineText(ln)); e != nil {
			continue
		}
		if e := m.match(lineText(ln)); e != nil {
			overrides[ln] = editToAuthor(e)
		}
	}

	// Blank separators inside the changed set inherit their nearest non-blank
	// changed neighbour (backward first, then forward) when that neighbour is AI.
	aiAt := func(ln int) (authorship.Author, bool) {
		if a, ok := overrides[ln]; ok {
			return a, true
		}
		if alreadyAI[ln] {
			for _, r := range wl.Lines {
				if ln >= r.Start && ln <= r.End && r.Author.Type == authorship.AI {
					return r.Author, true
				}
			}
		}
		return authorship.Author{}, false
	}
	// Walk CONTIGUOUS changed line numbers only (ln±1, …), stopping at the first
	// unchanged line, so a blank isolated by unchanged content never inherits a
	// distant block's attribution (same guard as inheritBlankAddRanges).
	blankLine := func(ln int) bool { return strings.TrimSpace(lineText(ln)) == "" }
	for _, ln := range changedSorted {
		if !blankLine(ln) || alreadyAI[ln] {
			continue
		}
		if _, ok := overrides[ln]; ok {
			continue
		}
		src, found := 0, false
		for k := ln - 1; changed[k]; k-- {
			if !blankLine(k) {
				src, found = k, true
				break
			}
		}
		if !found {
			for k := ln + 1; changed[k]; k++ {
				if !blankLine(k) {
					src, found = k, true
					break
				}
			}
		}
		if !found {
			continue
		}
		if a, ok := aiAt(src); ok {
			overrides[ln] = a
		}
	}

	if len(overrides) == 0 {
		return nil
	}
	return overrides
}
