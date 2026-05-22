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

	now := time.Now()
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
		if err := sink.Record(ev); err != nil {
			log.Printf("humanedit sink: %v", err)
		}
	}
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
