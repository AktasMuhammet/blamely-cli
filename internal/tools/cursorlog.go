package tools

// CursorLogWatcher supplements CursorWatcher by tailing Cursor's window /
// extension-host log files for AI-apply events. Cursor (being based on VS
// Code) writes structured JSON-ish logs at:
//
//   macOS  : ~/Library/Application Support/Cursor/logs/<date>/...
//   Linux  : ~/.config/Cursor/logs/<date>/...
//   Windows: %APPDATA%/Cursor/logs/<date>/...
//
// We look for lines that contain file-path strings alongside keywords like
// "composer", "apply", or "agent" — markers that Cursor's AI made a write.
// This is heuristic (the log schema is undocumented), but gives us a
// near-real-time Cursor signal that doesn't depend on history backups.
//
// Attribution from this watcher is confidence=medium (same as the history
// watcher) and covers the whole file, so the attribute join will only assign
// lines that actually appear in the commit diff.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/blamely/blamely/internal/config"
	"github.com/blamely/blamely/internal/daemon"
	"github.com/blamely/blamely/internal/gitutil"
)

// CursorLogWatcher tails Cursor log files for AI apply events.
type CursorLogWatcher struct {
	// LogsDir overrides the default for tests.
	LogsDir string
}

func (c *CursorLogWatcher) Name() string { return "cursor-log" }

func (c *CursorLogWatcher) Run(ctx context.Context, sink daemon.Sink) error {
	dir := c.LogsDir
	if dir == "" {
		var err error
		dir, err = cursorLogsDir()
		if err != nil {
			return err
		}
	}
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		log.Printf("cursor-log: %s not found, will poll", dir)
	}

	// Track which log files we've already attached a tailer to.
	tailers := map[string]context.CancelFunc{}
	defer func() {
		for _, cancel := range tailers {
			cancel()
		}
	}()

	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	for {
		files := findCursorLogFiles(dir)
		seen := map[string]bool{}
		for _, f := range files {
			seen[f] = true
			if _, running := tailers[f]; running {
				continue
			}
			tCtx, cancel := context.WithCancel(ctx)
			tailers[f] = cancel
			go func(path string) {
				if err := tailCursorLog(tCtx, path, sink); err != nil && tCtx.Err() == nil {
					log.Printf("cursor-log tail %s: %v", path, err)
				}
			}(f)
		}
		for path, cancel := range tailers {
			if !seen[path] {
				cancel()
				delete(tailers, path)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// findCursorLogFiles returns all .log files under dir (searching 2 levels deep
// so it catches logs/<date>/exthost/output/xxx.log etc.).
func findCursorLogFiles(dir string) []string {
	var out []string
	// Walk up to depth 4 (logs/<date>/<subdir>/<file>).
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(p, ".log") {
			// Only tail recent log files (modified within the last day).
			info, err := d.Info()
			if err == nil && time.Since(info.ModTime()) < 24*time.Hour {
				out = append(out, p)
			}
		}
		return nil
	})
	return out
}

// tailCursorLog streams a Cursor log file looking for apply-event lines.
// Each matching line tries to extract a file path and emits a cursor event.
func tailCursorLog(ctx context.Context, path string, sink daemon.Sink) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	// Seek to end on startup — we only want future log lines, not the history.
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("seek: %w", err)
	}

	r := bufio.NewReaderSize(f, 1<<16)
	backoff := 200 * time.Millisecond
	const maxBackoff = 2 * time.Second
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return err
			}
			// Partial line — wait for more data.
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
			continue
		}
		backoff = 200 * time.Millisecond
		if filePath, ok := extractCursorApplyPath(line); ok {
			emitCursorLogEvent(filePath, sink)
		}
	}
}

// extractCursorApplyPath looks for apply-event markers in a log line.
// Returns the file path if one is found.
func extractCursorApplyPath(line string) (string, bool) {
	lower := strings.ToLower(line)
	// Must look like an AI apply event — not just any log line.
	if !strings.Contains(lower, "composer") &&
		!strings.Contains(lower, "agentedit") &&
		!strings.Contains(lower, "applyedit") &&
		!strings.Contains(lower, "agent.apply") &&
		!strings.Contains(lower, "composerapply") {
		return "", false
	}
	// Extract the first file:// URI or /abs/path or C:\abs\path from the line.
	return extractFilePath(line)
}

// extractFilePath picks out the first plausible file path from a log line.
// It handles file:// URIs and bare absolute paths.
func extractFilePath(line string) (string, bool) {
	// Try file:// URI first.
	if i := strings.Index(line, "file:///"); i >= 0 {
		rest := line[i:]
		end := strings.IndexAny(rest, " \t\n\r\"'`}),")
		if end < 0 {
			end = len(rest)
		}
		uri := rest[:end]
		p := uriToPath(uri)
		if p != "" {
			return p, true
		}
	}
	// Bare absolute path (Unix: starts with /, Windows: C:\ etc.).
	for _, pfx := range []string{"/Users/", "/home/", "/var/", "/tmp/", "C:\\", "D:\\"} {
		if i := strings.Index(line, pfx); i >= 0 {
			rest := line[i:]
			end := strings.IndexAny(rest, " \t\n\r\"'`}),")
			if end < 0 {
				end = len(rest)
			}
			p := strings.TrimSpace(rest[:end])
			if len(p) > len(pfx) {
				return p, true
			}
		}
	}
	return "", false
}

func emitCursorLogEvent(abs string, sink daemon.Sink) {
	if _, err := os.Stat(abs); err != nil {
		return
	}
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
	lr, err := LineRangeForWholeFile(abs)
	if err != nil || lr == nil {
		return
	}
	ev := daemon.Event{
		When:       time.Now(),
		Tool:       "cursor",
		Confidence: "medium",
		GenType:    "chat", // cursor log events are AI apply events (Composer)
		RepoPath:   repo,
		FilePath:   rel,
		Lines:      []daemon.LineRange{{Start: lr.Start, End: lr.End, ContentSHA: lr.ContentSHA}},
		RawMeta:    `{"source":"cursor_log"}`,
	}
	if err := sink.Record(ev); err != nil {
		log.Printf("cursor-log sink: %v", err)
	}
}

func cursorLogsDir() (string, error) {
	home, err := config.Home()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Cursor", "logs"), nil
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "Cursor", "logs"), nil
		}
		return "", errors.New("APPDATA not set")
	default:
		return filepath.Join(home, ".config", "Cursor", "logs"), nil
	}
}
