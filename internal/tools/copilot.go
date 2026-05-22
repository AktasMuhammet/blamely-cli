package tools

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/blamely/blamely/internal/config"
	"github.com/blamely/blamely/internal/daemon"
)

// CopilotWatcher is a best-effort, low-confidence detector.
//
// Copilot has no usable local hook for inline suggestions. v1's approach:
//
//   - Verify Copilot is *installed* (extension dir present).
//   - Watch the VS Code globalStorage for `github.copilot*` updates. When the
//     storage SQLite is modified, the user almost certainly accepted a Copilot
//     suggestion or completed a Copilot Chat exchange. We can't know which
//     file or which lines, so we record a low-confidence "session active"
//     marker.
//
// The attribute step uses these markers only as a TIE-BREAKER: when a line
// has no Claude/Cursor/Codex attribution AND a Copilot session marker exists
// within ±60s of the line's last on-disk modification, the line is recorded
// with tool=copilot, confidence=low. Otherwise it stays human.
//
// This is intentionally cautious — better to miss some Copilot attributions
// than to incorrectly steal credit from human typing.
type CopilotWatcher struct {
	// StorageRoots overrides the storage dirs for tests.
	StorageRoots []string
}

func (c *CopilotWatcher) Name() string { return "copilot" }

func (c *CopilotWatcher) Run(ctx context.Context, sink daemon.Sink) error {
	roots := c.StorageRoots
	if len(roots) == 0 {
		roots = defaultCopilotStorageRoots()
	}
	// Track the latest mtime we've seen per file, so we only emit on change.
	lastMtime := map[string]time.Time{}

	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()

	for {
		for _, root := range roots {
			c.scan(root, lastMtime, sink)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

func (c *CopilotWatcher) scan(root string, lastMtime map[string]time.Time, sink daemon.Sink) {
	if root == "" {
		return
	}
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		mt := info.ModTime()
		prev := lastMtime[p]
		lastMtime[p] = mt
		if prev.IsZero() {
			// First scan — prime, don't emit.
			return nil
		}
		if !mt.After(prev) {
			return nil
		}
		// File mutated since last scan → record a session-active marker.
		// RepoPath/FilePath are intentionally empty; this is a global signal,
		// not a per-file attribution. The attribute step will fold it in as
		// a tie-breaker.
		ev := daemon.Event{
			When:       mt,
			Tool:       "copilot",
			Confidence: "low",
			GenType:    "chat", // storage-touch = Copilot Chat panel session
			RawMeta:    `{"source":"copilot_storage_touch"}`,
		}
		if err := sink.Record(ev); err != nil {
			log.Printf("copilot sink: %v", err)
		}
		return nil
	})
}

func defaultCopilotStorageRoots() []string {
	home, err := config.Home()
	if err != nil {
		return nil
	}
	roots := []string{
		filepath.Join(home, ".config", "github-copilot"),
	}
	switch runtime.GOOS {
	case "darwin":
		roots = append(roots,
			filepath.Join(home, "Library", "Application Support", "Code", "User", "globalStorage", "github.copilot"),
			filepath.Join(home, "Library", "Application Support", "Code", "User", "globalStorage", "github.copilot-chat"),
			filepath.Join(home, "Library", "Application Support", "Cursor", "User", "globalStorage", "github.copilot"),
			filepath.Join(home, "Library", "Application Support", "Cursor", "User", "globalStorage", "github.copilot-chat"),
		)
	case "windows":
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			roots = append(roots,
				filepath.Join(local, "Programs", "Microsoft VS Code", "User", "globalStorage", "github.copilot"),
				filepath.Join(local, "github-copilot"),
			)
		}
	default:
		roots = append(roots,
			filepath.Join(home, ".config", "Code", "User", "globalStorage", "github.copilot"),
			filepath.Join(home, ".config", "Code", "User", "globalStorage", "github.copilot-chat"),
		)
	}
	return roots
}
