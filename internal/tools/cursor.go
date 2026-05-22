package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/blamely/blamely/internal/config"
	"github.com/blamely/blamely/internal/daemon"
	"github.com/blamely/blamely/internal/gitutil"
)

// CursorWatcher attributes file changes to Cursor when they coincide with a
// Cursor "History" backup write. Cursor maintains an undo history at:
//
//   macOS  : ~/Library/Application Support/Cursor/User/History/
//   Linux  : ~/.config/Cursor/User/History/
//   Windows: %APPDATA%/Cursor/User/History/
//
// Each history entry is a directory containing an entries.json (the workspace
// file path) and one or more snapshot files. When Cursor writes a new
// snapshot, we read entries.json to find which workspace file was modified
// and record an attribution event.
//
// Confidence is HIGH when Cursor is the only IDE active on the file (the
// History backup is unambiguous), but we can't easily distinguish manual
// typing in Cursor from AI Composer/Apply edits without parsing Cursor's
// internal workspace SQLite. So v1 records ALL Cursor-edited lines with
// confidence=medium and notes "all-cursor (AI or manual)" in raw_meta.
type CursorWatcher struct {
	// HistoryDir overrides the default for tests.
	HistoryDir string
}

func (c *CursorWatcher) Name() string { return "cursor" }

func (c *CursorWatcher) Run(ctx context.Context, sink daemon.Sink) error {
	dir := c.HistoryDir
	if dir == "" {
		var err error
		dir, err = cursorHistoryDir()
		if err != nil {
			return err
		}
	}
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		log.Printf("cursor: %s not found, will poll", dir)
	}

	// In-memory record of which (history-dir, snapshot-id) pairs we've seen,
	// so a scan only emits an event for genuinely new snapshots.
	seen := map[string]bool{}

	// primeOlderThan: on the first scan we emit history snapshots that were
	// written within the last hour, even though they existed before the daemon
	// started. This makes a daemon restart retroactively capture Cursor edits
	// that happened while the daemon was down (up to 1 hour ago).
	primeOlderThan := time.Now().Add(-1 * time.Hour)

	// Prime scan: emit recent historical snapshots using their mtime as `When`
	// so they slot into the chronological record. After this point, live
	// snapshots use time.Now() so they win newest-first against humanedit
	// rows that fsnotify on the workspace file produces in the same instant.
	c.scan(dir, seen, true, primeOlderThan, sink)

	// fsnotify the history tree so we react to new snapshots within
	// milliseconds, not on a 2-second poll. The 2s ticker remains as a safety
	// net for edge cases (e.g. new entry subdir created on a platform where
	// the parent watcher misses it, or fsnotify dropping events under load).
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer fw.Close()
	c.addHistoryWatches(fw, dir)

	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-fw.Events:
			if !ok {
				return nil
			}
			// New entry directory appeared — start watching it so we see its
			// snapshot files. Then run a scan (the snapshot file may already
			// exist by the time we get here).
			if ev.Op&fsnotify.Create != 0 {
				if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
					_ = fw.Add(ev.Name)
				}
			}
			c.scan(dir, seen, false, primeOlderThan, sink)
		case err, ok := <-fw.Errors:
			if !ok {
				return nil
			}
			log.Printf("cursor watcher: %v", err)
		case <-tick.C:
			// Safety-net scan + re-attach watches for any subdirs created
			// while the watcher was unhealthy.
			c.addHistoryWatches(fw, dir)
			c.scan(dir, seen, false, primeOlderThan, sink)
		}
	}
}

// addHistoryWatches attaches a fsnotify watch to the history root and each
// existing entry subdirectory. Idempotent — re-adding a watched path is a
// no-op-ish operation that fsnotify tolerates.
func (c *CursorWatcher) addHistoryWatches(w *fsnotify.Watcher, historyDir string) {
	if err := w.Add(historyDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		// History dir doesn't exist yet (Cursor not installed / first-run).
		// The ticker will retry.
		log.Printf("cursor watcher: add %s: %v", historyDir, err)
	}
	entries, err := os.ReadDir(historyDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		_ = w.Add(filepath.Join(historyDir, e.Name()))
	}
}

func (c *CursorWatcher) scan(historyDir string, seen map[string]bool, primeOnly bool, primeOlderThan time.Time, sink daemon.Sink) {
	entries, err := os.ReadDir(historyDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		entryDir := filepath.Join(historyDir, e.Name())
		entriesJSON := filepath.Join(entryDir, "entries.json")
		manifest, err := readCursorEntries(entriesJSON)
		if err != nil || manifest == nil {
			continue
		}
		snapshots, _ := os.ReadDir(entryDir)
		for _, s := range snapshots {
			if s.IsDir() || s.Name() == "entries.json" {
				continue
			}
			key := entryDir + "::" + s.Name()
			if seen[key] {
				continue
			}
			seen[key] = true
			info, err := s.Info()
			if err != nil {
				continue
			}
			mt := info.ModTime()
			// On the prime (startup) scan, only emit snapshots that are recent
			// enough to be relevant (within the last hour). Older ones are
			// ancient history we don't want to retroactively attribute. New
			// snapshots (from the live fsnotify path) are always emitted.
			if primeOnly && mt.Before(primeOlderThan) {
				continue
			}
			// `When` controls newest-first ordering in attribution. For prime
			// snapshots we use mtime so they sit at their real point in
			// history. For LIVE snapshots we use time.Now() — otherwise the
			// snapshot's mtime (the OLD content backup, written microseconds
			// before the workspace file write) ends up older than the
			// humanedit watcher's row for the same file, and humanedit wins
			// the newest-first join even though the change came from Cursor.
			when := mt
			if !primeOnly {
				when = time.Now()
			}
			c.emit(manifest, filepath.Join(entryDir, s.Name()), when, sink)
		}
	}
}

// cursorEntries is the subset of entries.json we need. Cursor writes:
//
//	{
//	  "version": 1,
//	  "resource": "file:///abs/path/to/workspace-file.go",
//	  "entries": [
//	    { "id": "abc.go", "timestamp": 1700000000000 },
//	    ...
//	  ]
//	}
type cursorEntries struct {
	Resource string `json:"resource"`
	Entries  []struct {
		ID        string `json:"id"`
		Source    string `json:"source"`
		Timestamp int64  `json:"timestamp"`
	} `json:"entries"`
}

func readCursorEntries(path string) (*cursorEntries, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ce cursorEntries
	if err := json.Unmarshal(data, &ce); err != nil {
		return nil, err
	}
	return &ce, nil
}

func (c *CursorWatcher) emit(manifest *cursorEntries, snapshot string, when time.Time, sink daemon.Sink) {
	abs := uriToPath(manifest.Resource)
	if abs == "" {
		return
	}
	if _, err := os.Stat(abs); err != nil {
		// File was deleted/moved — skip.
		return
	}
	// Resolve symlinks so the file path matches the worktree's resolved root.
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		abs = r
	}
	repo, _ := gitutil.RepoID(abs)
	wt, _ := gitutil.Toplevel(abs)
	rel := abs
	if wt != "" {
		if r, err := filepath.Rel(wt, abs); err == nil && !strings.HasPrefix(r, "..") {
			rel = r
		}
	}

	// We don't know which lines were touched by the AI vs manually. v1 marks
	// the whole file as cursor-touched at the time of the snapshot, with
	// medium confidence. The attribute step will only assign to cursor the
	// lines that ALSO appear in the commit diff — manual interleaving is
	// captured implicitly because any line later touched by the human (after
	// the snapshot) will get a more-recent edit row from fsnotify (future).
	lr, err := LineRangeForWholeFile(abs)
	if err != nil || lr == nil {
		return
	}
	ev := daemon.Event{
		When:       when,
		Tool:       "cursor",
		Confidence: "medium",
		GenType:    "chat", // Cursor history = Composer session (chat panel)
		RepoPath:   repo,
		FilePath:   rel,
		Lines:      []daemon.LineRange{{Start: lr.Start, End: lr.End, ContentSHA: lr.ContentSHA}},
		RawMeta:    fmt.Sprintf(`{"source":"cursor_history","snapshot":%q}`, filepath.Base(snapshot)),
	}
	if err := sink.Record(ev); err != nil {
		log.Printf("cursor sink: %v", err)
	}
}

// uriToPath decodes a "file:///" URI into a local path. Returns "" if the
// resource isn't a local file. Handles macOS/Linux & basic Windows forms.
func uriToPath(uri string) string {
	const prefix = "file://"
	if !strings.HasPrefix(uri, prefix) {
		return ""
	}
	p := uri[len(prefix):]
	// "file:///Users/..." → "/Users/..."
	// "file://C:/..."     → "C:/..."  (Windows form)
	if strings.HasPrefix(p, "/") && runtime.GOOS == "windows" && len(p) > 3 && p[2] == ':' {
		p = p[1:]
	}
	// Percent-decode the simplest cases. We deliberately don't pull net/url to
	// avoid hauling that in for one helper; spaces and `%20` are the common ones.
	p = strings.ReplaceAll(p, "%20", " ")
	return p
}

func cursorHistoryDir() (string, error) {
	home, err := config.Home()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Cursor", "User", "History"), nil
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "Cursor", "User", "History"), nil
		}
		return "", errors.New("APPDATA not set")
	default:
		return filepath.Join(home, ".config", "Cursor", "User", "History"), nil
	}
}
