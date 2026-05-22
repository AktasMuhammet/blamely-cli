package tools

// HumanEditWatcher detects user-typed (non-AI) edits and records them as
// `tool=human` rows so the attribution step's newest-edit-wins logic credits
// the user when they modify lines an AI tool previously claimed.
//
// Why this matters: an AI's PostToolUse hook records "claude wrote lines
// 1..10" at time T1. If the user then edits line 5 at T2 (> T1), no other
// component writes a row covering line 5 with tool=human, so the per-line
// join in internal/gitnotes/attribute.go silently keeps the AI's claim. This
// watcher is the missing link.
//
// Algorithm:
//   1. fsnotify the dir of every repo the daemon has already observed (same
//      seed list as VelocityWatcher).
//   2. On each file write, compute the line ranges that newly appeared in the
//      file content vs. the previous snapshot via AddedOrChangedRanges.
//   3. For each changed range, query the DB for any AI edit recorded on this
//      file in the last `aiClaimWindow` seconds. If an AI edit's lines
//      overlap the range, skip — that change is the AI's own write. Otherwise
//      record a `tool=human, confidence=high, gen_type=unknown` edit for the
//      range so the attribution join can credit the user.
//
// Memory: we cache the full file content per observed file (bounded by a
// per-file max so a giant generated file doesn't pin gigabytes). For typical
// source trees this is in the tens of MB. The cache is process-local; it's
// rebuilt on daemon restart, which means the first edit after restart can be
// missed. That's acceptable — the trade-off vs. a persistent cache.

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/blamely/blamely/internal/daemon"
	"github.com/blamely/blamely/internal/gitutil"
	"github.com/blamely/blamely/internal/store"
)

const (
	// aiClaimWindow is how recently an AI hook had to fire for us to assume
	// the change we just observed is the AI's own write (not a human edit
	// on top of it). 3s is generous: hooks fire synchronously with the
	// file write, so a real overlap should be well under 1s.
	aiClaimWindow = 3 * time.Second
	// copilotSessionWindow widens the AI-claim window specifically for the
	// Copilot session-active marker. The Copilot watcher polls global
	// storage every 5s, so a chat-panel paste can produce a marker that
	// arrives several seconds after the file write. Without this widening
	// we'd record the paste as `tool=human` and the attribute step's
	// noEdit-only fold-in can't recover it.
	copilotSessionWindow = 15 * time.Second
	// humanEditMaxFileBytes caps per-file cache size so a 10 MB generated
	// file doesn't dominate memory. Files above this size aren't tracked.
	humanEditMaxFileBytes = 1 << 20 // 1 MiB
	// humanEditPollRefresh is how often we re-seed the watched dirs from
	// the DB (new repos appear as the user works in them).
	humanEditPollRefresh = 30 * time.Second
)

// HumanEditWatcher implements daemon.Watcher.
type HumanEditWatcher struct {
	DB *store.DB
}

func (h *HumanEditWatcher) Name() string { return "humanedit" }

func (h *HumanEditWatcher) Run(ctx context.Context, sink daemon.Sink) error {
	if h.DB == nil {
		return errors.New("humanedit watcher: DB is required")
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()

	mu := sync.Mutex{}
	cache := map[string][]byte{} // file path → last-observed content (≤ max bytes)

	watched := map[string]bool{}
	h.addKnownRepos(w, watched)

	refresh := time.NewTicker(humanEditPollRefresh)
	defer refresh.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-refresh.C:
			h.addKnownRepos(w, watched)

		case evt, ok := <-w.Events:
			if !ok {
				return nil
			}
			if evt.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}
			path := evt.Name
			if !looksLikeSourceFile(path) {
				continue
			}
			go h.handleChange(path, sink, &mu, cache)

		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			log.Printf("humanedit watcher: %v", err)
		}
	}
}

func (h *HumanEditWatcher) addKnownRepos(w *fsnotify.Watcher, watched map[string]bool) {
	repos, err := h.DB.KnownRepoPaths()
	if err != nil {
		return
	}
	for _, repo := range repos {
		if watched[repo] {
			continue
		}
		if err := w.Add(repo); err == nil {
			watched[repo] = true
		}
	}
}

func (h *HumanEditWatcher) handleChange(
	path string,
	sink daemon.Sink,
	mu *sync.Mutex,
	cache map[string][]byte,
) {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return
	}
	if st.Size() > humanEditMaxFileBytes {
		// File too big to track diffs efficiently; drop from cache so we
		// re-prime if it shrinks later.
		mu.Lock()
		delete(cache, path)
		mu.Unlock()
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}

	mu.Lock()
	prev, hadPrev := cache[path]
	cache[path] = data
	mu.Unlock()

	if !hadPrev {
		// First time we've seen this file — prime the cache and emit nothing.
		// Pre-existing content is treated as "unknown origin", not human.
		return
	}

	changed := AddedOrChangedRanges(prev, data)
	if len(changed) == 0 {
		return
	}

	// Resolve repo + relative file path. Skip if not inside a git repo.
	abs := path
	if r, err := filepath.EvalSymlinks(path); err == nil {
		abs = r
	}
	repoID, _ := gitutil.RepoID(abs)
	if repoID == "" {
		return
	}
	wt, _ := gitutil.Toplevel(abs)
	rel := abs
	if wt != "" {
		if r, err := filepath.Rel(wt, abs); err == nil && !strings.HasPrefix(r, "..") {
			rel = r
		}
	}

	// Look up any AI edits on this file in the recent past. If one of them
	// covers a changed range, that range is the AI's own write — don't
	// record a competing human row.
	sinceNanos := time.Now().Add(-aiClaimWindow).UnixNano()
	aiEdits, err := h.DB.EditsForFileSince(repoID, rel, sinceNanos)
	if err != nil {
		log.Printf("humanedit: db lookup: %v", err)
		aiEdits = nil
	}

	// Copilot chat-panel pastes don't carry a file path in their hook
	// payload (or no hook fires at all for some Copilot variants), so the
	// only signal we have is a global "Copilot was active" marker emitted
	// by CopilotWatcher when its globalStorage SQLite mutates. The marker
	// has no Lines, which means rangeClaimedByAI can't match it. Look it
	// up separately and, when present, attribute the just-observed change
	// to Copilot instead of the user.
	now := time.Now()
	copilotActive := h.DB.HasCopilotSessionNear(
		now.UnixNano(),
		int64(copilotSessionWindow),
	)
	// If the CopilotChatWatcher recently emitted a model-tagged marker
	// (parsed from the chat-session JSONL), pick that up so the copilot
	// row we're about to write carries the model the user actually had
	// selected in the chat panel. Empty string is fine — downstream just
	// skips the model field in the note.
	copilotModel := ""
	// gen_type should reflect what the user actually did: a chat-panel
	// session marker → "chat"; a Copilot-log accept line → "completion".
	// We pull the latest specific marker's gen_type; if nothing more
	// specific than the globalStorage-mutation marker exists, default
	// to "completion" since Tab/inline is the dominant case.
	copilotGenType := ""
	if copilotActive {
		copilotModel = h.DB.LatestCopilotModelNear(now.UnixNano(), int64(copilotSessionWindow))
		copilotGenType = h.DB.LatestCopilotGenTypeNear(now.UnixNano(), int64(copilotSessionWindow))
		if copilotGenType == "" {
			copilotGenType = string(store.GenTypeCompletion)
		}
	}
	// Clipboard sample: read once per file-change event so we can detect
	// pastes (claude.ai / chatgpt / Gemini / another project / Stack
	// Overflow / etc.). An empty string means the clipboard is unavailable
	// or doesn't match — pasteMatchesClipboard handles that gracefully.
	clipboard := ReadClipboard()
	for _, r := range changed {
		if rangeClaimedByAI(r, aiEdits) {
			continue
		}
		ev := daemon.Event{
			When:       now,
			Tool:       string(store.ToolHuman),
			Confidence: string(store.ConfidenceHigh),
			GenType:    string(store.GenTypeUnknown),
			RepoPath:   repoID,
			FilePath:   rel,
			Lines:      []daemon.LineRange{{Start: r.Start, End: r.End, ContentSHA: r.ContentSHA}},
			RawMeta:    `{"source":"humanedit_watcher"}`,
		}
		switch {
		case copilotActive:
			// Re-stamp the row as a low-confidence Copilot edit. We use
			// the concrete line range from the file diff (better than
			// the watcher's whole-file/empty signal). Confidence=low
			// keeps it below hook-recorded copilot rows in priority if
			// both land for the same lines. gen_type follows the latest
			// specific Copilot marker (chat vs completion) instead of
			// being hard-coded.
			ev.Tool = string(store.ToolCopilot)
			ev.Confidence = string(store.ConfidenceLow)
			ev.GenType = copilotGenType
			ev.Model = copilotModel
			ev.RawMeta = `{"source":"humanedit_watcher","copilot_session":true}`
		case pasteMatchesClipboard(data, r, clipboard):
			// The just-inserted text appears in the clipboard — almost
			// certainly a paste, not typed. The SOURCE of the clipboard
			// is unknown (could be a web AI chat, another project, Stack
			// Overflow, …), so we don't claim AI origin. We just record
			// `tool=copypaste` so reports can show it distinctly from
			// typed-by-user code.
			ev.Tool = string(store.ToolCopyPaste)
			ev.Confidence = string(store.ConfidenceMedium)
			ev.GenType = string(store.GenTypeUnknown)
			ev.RawMeta = `{"source":"humanedit_watcher","clipboard_match":true}`
		}
		if err := sink.Record(ev); err != nil {
			log.Printf("humanedit sink: %v", err)
		}
	}
}

// pasteMatchesClipboard reports true when the text on lines [r.Start..r.End]
// of `data` appears (after whitespace/blank-line normalization) inside the
// current clipboard text. It only matches multi-line inserts — single-line
// matches are too prone to coincidence (`}`, `return nil`, etc.) — and
// requires at least 20 chars of normalized content to be meaningful.
func pasteMatchesClipboard(data []byte, r LineRange, clipboard string) bool {
	if clipboard == "" || r.End < r.Start {
		return false
	}
	rangeText := extractLineRange(data, r.Start, r.End)
	normRange := normalizeForClipboardMatch(rangeText)
	if normRange == "" {
		return false
	}
	// Require multi-line + a minimum content size so we don't flag a
	// single common token (`}`) on the clipboard as a paste.
	if !strings.Contains(normRange, "\n") || len(normRange) < 20 {
		return false
	}
	normClip := normalizeForClipboardMatch(clipboard)
	if normClip == "" {
		return false
	}
	return strings.Contains(normClip, normRange)
}

// extractLineRange returns the inclusive 1-based [start..end] slice of `data`
// joined by '\n'. Out-of-range start/end are clamped. Mirrors the line
// indexing AddedOrChangedRanges produces so we can re-extract the text the
// watcher flagged as changed.
func extractLineRange(data []byte, start, end int) string {
	if start < 1 {
		start = 1
	}
	lines := strings.Split(string(data), "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	if end > len(lines) {
		end = len(lines)
	}
	if start > end {
		return ""
	}
	return strings.Join(lines[start-1:end], "\n")
}

// normalizeForClipboardMatch strips per-line leading/trailing whitespace and
// drops blank lines, so an editor's auto-indent reflow on paste doesn't
// defeat the substring check. The returned string preserves line order and
// uses '\n' as a separator.
func normalizeForClipboardMatch(s string) string {
	parts := strings.Split(s, "\n")
	out := parts[:0]
	for _, l := range parts {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// rangeClaimedByAI reports true when an AI edit row covers the entirety of
// `r`. (Partial overlap → still emit a human row for the user-typed portion.
// Since we don't currently split the range further, the entire range is
// recorded as human; the AI's pre-existing row covers what it actually
// produced, so newest-wins still gives the human credit only where they
// actually edited.)
func rangeClaimedByAI(r LineRange, aiEdits []store.Edit) bool {
	for _, e := range aiEdits {
		if e.Tool == store.ToolHuman {
			continue
		}
		for _, el := range e.Lines {
			if el.StartLine <= r.Start && el.EndLine >= r.End {
				return true
			}
		}
	}
	return false
}
