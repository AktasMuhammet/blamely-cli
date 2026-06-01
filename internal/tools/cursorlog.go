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

// isCursorTabLog reports whether path looks like a Cursor Tab completion log.
// Cursor Tab writes its output to a file named "Cursor Tab.log" inside an
// extension directory that matches "cursor-always-local".
func isCursorTabLog(path string) bool {
	base := filepath.Base(path)
	dir := filepath.Base(filepath.Dir(path))
	return base == "Cursor Tab.log" || strings.Contains(dir, "cursor-always-local")
}

// tailCursorLog streams a Cursor log file looking for AI-apply events.
//
// For Composer/Agent apply events: emits confidence=medium cursor/chat records
// with the applied file path and whole-file line range.
//
// For Cursor Tab logs (isCursorTabLog): the log entry fires on suggestion
// GENERATION, not user acceptance, so we cannot claim specific lines. We do
// emit a low-confidence session marker (no file, no lines) so the daemon knows
// Cursor Tab was recently active — this aids model backfill and debugging.
// Precise Tab attribution (file + lines) requires the Blamely editor plugin,
// which listens to editor.action.inlineSuggest.commit.
func tailCursorLog(ctx context.Context, path string, sink daemon.Sink) error {
	if isCursorTabLog(path) {
		return tailCursorTabLog(ctx, path, sink)
	}

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

// tailCursorTabLog tails a Cursor Tab completion log and emits a low-confidence
// session marker each time Cursor Tab generates a suggestion. The marker has no
// file or line context — the log fires on suggestion GENERATION, not on user
// acceptance, so we cannot claim specific lines from it.
//
// The session marker's purpose is narrow: it lets the daemon know Cursor Tab
// was recently active (for model backfill and debug visibility). Precise
// Tab attribution requires the Blamely editor plugin.
func tailCursorTabLog(ctx context.Context, path string, sink daemon.Sink) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("seek: %w", err)
	}

	r := bufio.NewReaderSize(f, 1<<16)
	backoff := 200 * time.Millisecond
	const maxBackoff = 2 * time.Second
	modelOutputPending := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return err
			}
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

		if strings.Contains(line, "=======>Model output") {
			modelOutputPending = true
			continue
		}
		if modelOutputPending {
			if _, ok := extractCursorTabPath(line); ok {
				modelOutputPending = false
				// Emit a session marker — no file/lines because this is a
				// generation event, not an acceptance event. The marker records
				// "Cursor Tab was active at this moment" for the daemon.
				ev := daemon.Event{
					When:       time.Now(),
					Tool:       "cursor",
					Confidence: "low",
					GenType:    "completion",
					RawMeta:    `{"source":"cursor_tab_log"}`,
				}
				if err := sink.Record(ev); err != nil {
					log.Printf("cursor-tab sink: %v", err)
				}
			} else if !strings.HasPrefix(strings.TrimSpace(line), "@@") {
				modelOutputPending = false
			}
		}
	}
}

// extractCursorTabPath parses the `@@ filepath:line` diff-header line that
// Cursor Tab writes immediately after "=======>Model output" to identify
// which file is being completed. Returns the file path on success.
func extractCursorTabPath(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "@@") {
		return "", false
	}
	// Format: "@@ src/main/java/.../Foo.java:72" (one @@ pair, then the path).
	rest := strings.TrimPrefix(trimmed, "@@")
	rest = strings.TrimSpace(rest)
	// Strip a trailing colon+line-number if present.
	if i := strings.LastIndex(rest, ":"); i > 0 {
		candidate := rest[:i]
		if _, err := fmt.Sscanf(rest[i+1:], "%d", new(int)); err == nil {
			rest = candidate
		}
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", false
	}
	return rest, true
}

// extractCursorApplyPath looks for apply-event markers in a log line.
// Returns the file path if one is found.
//
// The keyword set is intentionally narrow: it must look like an explicit
// Composer or Agent APPLY event. We deliberately exclude the generic
// "applyedit" / "applyEdit" keyword even though Cursor logs it for AI
// applies — VS Code also emits applyEdit for Tab completions, formatter
// rewrites, auto-import inserts, and other non-AI edits. Matching it
// caused two compounding bugs:
//
//  1. A Tab completion's applyEdit log line (written AFTER the file save)
//     emitted a whole-file cursor/chat row newer than the humanedit row for
//     manually-typed lines in the same session, overriding their attribution.
//
//  2. The gen_type was hard-coded to "chat" in emitCursorLogEvent, so Tab
//     completions showed up as "chat" instead of "completion".
//
// Tab completion attribution is handled by the editor plugin
// (Cursor/VS Code DocumentListener → /edit endpoint).
func extractCursorApplyPath(line string) (string, bool) {
	lower := strings.ToLower(line)
	if !strings.Contains(lower, "composerapply") &&
		!strings.Contains(lower, "composer.apply") &&
		!strings.Contains(lower, "agentedit") &&
		!strings.Contains(lower, "agent.edit") &&
		!strings.Contains(lower, "agent.apply") {
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

// DebugCursorLogs tails Cursor's log files and prints detected AI-apply
// events to out. It is the backing implementation of `blamely log cursor`.
//
// When debug is false (default) only lines that match an AI-apply keyword are
// printed. When debug is true every scanned line is printed, prefixed with
// "[MATCH]" or "[skip]", so users can trace why a Cursor Composer action was
// or was not detected.
//
// The function blocks until ctx is cancelled; it is safe to call from a
// cobra RunE with cmd.Context().
func DebugCursorLogs(ctx context.Context, debug bool, out io.Writer) error {
	dir, err := cursorLogsDir()
	if err != nil {
		return fmt.Errorf("cursor logs dir: %w", err)
	}
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("Cursor log directory not found: %s\n"+
			"Is Cursor installed and has it been opened at least once?", dir)
	}

	fmt.Fprintf(out, "Scanning Cursor logs in %s\n", dir)
	if debug {
		fmt.Fprintf(out, "Debug mode: all scanned lines are shown.\n")
		fmt.Fprintf(out, "  [MATCH]            = Composer/Agent apply event (recorded by daemon)\n")
		fmt.Fprintf(out, "  [Tab shown]        = Cursor Tab suggestion shown — session marker emitted (low confidence).\n")
		fmt.Fprintf(out, "                       For precise line attribution install the Blamely editor plugin.\n")
		fmt.Fprintf(out, "  [skip]             = line scanned, no apply keyword found\n")
	} else {
		fmt.Fprintf(out, "Showing detected AI-apply events only (use --debug to see all lines).\n")
	}
	fmt.Fprintln(out)

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
		for _, f := range files {
			if _, running := tailers[f]; running {
				continue
			}
			tCtx, cancel := context.WithCancel(ctx)
			tailers[f] = cancel
			go func(path string) {
				if err := debugTailCursorLog(tCtx, path, debug, out); err != nil && tCtx.Err() == nil {
					fmt.Fprintf(out, "[error] %s: %v\n", path, err)
				}
			}(f)
		}
		// Prune tailers for files that have gone away.
		seen := make(map[string]bool, len(files))
		for _, f := range files {
			seen[f] = true
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

// debugTailCursorLog tails one log file and writes each line to out.
// In debug mode every line is printed; otherwise only matching lines.
// Handles both Composer apply logs and Cursor Tab completion logs.
func debugTailCursorLog(ctx context.Context, path string, debug bool, out io.Writer) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	// Seek to end so we only show new lines written after the command starts.
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("seek: %w", err)
	}

	isTabLog := isCursorTabLog(path)
	shortPath := filepath.Base(filepath.Dir(path)) + "/" + filepath.Base(path)
	r := bufio.NewReaderSize(f, 1<<16)
	backoff := 200 * time.Millisecond
	const maxBackoff = 2 * time.Second
	modelOutputPending := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return err
			}
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
		trimmed := strings.TrimRight(line, "\r\n")

		if isTabLog {
			if strings.Contains(line, "=======>Model output") {
				modelOutputPending = true
				if debug {
					fmt.Fprintf(out, "[wait]  %s  %s\n", shortPath, trimmed)
				}
				continue
			}
			if modelOutputPending {
				if filePath, ok := extractCursorTabPath(line); ok {
					modelOutputPending = false
					fmt.Fprintf(out, "[Tab shown] %s  →  %s  (session marker emitted, no lines — install plugin for line attribution)\n", shortPath, filePath)
				} else {
					if !strings.HasPrefix(strings.TrimSpace(line), "@@") {
						modelOutputPending = false
					}
					if debug {
						fmt.Fprintf(out, "[skip]  %s  %s\n", shortPath, trimmed)
					}
				}
				continue
			}
		} else {
			if filePath, ok := extractCursorApplyPath(line); ok {
				fmt.Fprintf(out, "[MATCH] %s  →  %s\n", shortPath, filePath)
				continue
			}
		}

		if debug {
			fmt.Fprintf(out, "[skip]  %s  %s\n", shortPath, trimmed)
		}
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
