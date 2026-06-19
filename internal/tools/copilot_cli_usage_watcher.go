package tools

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/blamely/blamely/internal/config"
	"github.com/blamely/blamely/internal/daemon"
	"github.com/blamely/blamely/internal/store"
)

// copilotCliUsageStaleCutoff: ignore session dirs untouched longer than this.
// A session's totals only land in its terminal session.shutdown event, so we
// only care about recently-active sessions; ancient ones are already recorded.
const copilotCliUsageStaleCutoff = 30 * 24 * time.Hour

const copilotCliUsagePollInterval = 30 * time.Second

// CopilotCliUsageWatcher records the Copilot CLI's per-session, per-model
// CUMULATIVE token totals into the session_usage table. Unlike the per-edit hook
// path (ReadCopilotCliUsage), this captures the full session spend — input,
// cache and reasoning tokens included — which the CLI only emits at session end
// (session.shutdown). It is a session-level metric, never attached to an edit.
type CopilotCliUsageWatcher struct {
	// SessionStateDir overrides ~/.copilot/session-state for tests.
	SessionStateDir string
	DB              *store.DB
}

func (c *CopilotCliUsageWatcher) Name() string { return "copilot-cli-usage" }

func (c *CopilotCliUsageWatcher) Run(ctx context.Context, _ daemon.Sink) error {
	if c.DB == nil {
		return nil // no DB → nothing to persist into
	}
	dir := c.SessionStateDir
	if dir == "" {
		var err error
		if dir, err = config.CopilotSessionStateDir(); err != nil {
			return err
		}
	}

	tick := time.NewTicker(copilotCliUsagePollInterval)
	defer tick.Stop()
	c.scan(dir) // initial pass so we don't wait a full interval on startup
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			c.scan(dir)
		}
	}
}

// scan records session totals for any session whose events log has changed since
// we last looked. The watermark stores the byte size we've already scanned
// through, so unchanged logs are skipped and a restart resumes cleanly.
func (c *CopilotCliUsageWatcher) scan(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // dir absent (CLI not installed) → nothing to do
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sessionID := e.Name()
		p := filepath.Join(dir, sessionID, "events.jsonl")
		info, err := os.Stat(p)
		if err != nil || time.Since(info.ModTime()) > copilotCliUsageStaleCutoff {
			continue
		}
		// Skip logs we've already scanned through at their current size.
		if wm, ok := c.DB.LoadWatermark(c.Name(), p); ok && wm.ByteOffset >= info.Size() && wm.Size == info.Size() {
			continue
		}
		c.recordSession(p, sessionID, info.Size(), info.ModTime())
	}
}

func (c *CopilotCliUsageWatcher) recordSession(path, sessionID string, size int64, mtime time.Time) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	usage, err := scanCopilotCliSessionUsage(f)
	if err != nil {
		log.Printf("copilot-cli-usage: scan %s: %v", path, err)
		return
	}
	for model, u := range usage {
		if model == "" {
			continue
		}
		if err := c.DB.SaveSessionUsage(sessionID, "copilot", model, u); err != nil {
			log.Printf("copilot-cli-usage: save %s/%s: %v", sessionID, model, err)
		}
	}
	// Mark this log scanned through its current size, even if no shutdown was
	// found yet (a still-running session) — we'll re-scan once it grows further.
	_ = c.DB.SaveWatermark(c.Name(), path, store.Watermark{
		ByteOffset: size, Size: size, MtimeNanos: mtime.UnixNano(),
	})
}
