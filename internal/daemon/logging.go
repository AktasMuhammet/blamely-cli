package daemon

import (
	"bytes"
	"context"
	"io"
	"log"
	"os"
	"sync"
	"time"

	"github.com/blamely/blamely/internal/config"
)

// logRetention is how much history daemon.log keeps. Older lines are pruned on
// startup and hourly thereafter, so the file can't grow without bound.
const logRetention = 24 * time.Hour

// logTimeLayout matches the prefix the standard log package writes with
// log.LstdFlags ("2006/01/02 15:04:05 ..."), in local time. The pruner parses
// it to decide a line's age.
const logTimeLayout = "2006/01/02 15:04:05"

// setupLogging routes the standard log package to ~/.blamely/daemon.log and
// starts the 24h retention pruner. Without this the daemon only logged to
// stderr, which the macOS/Linux service definitions redirect to daemon.log —
// but the Windows launcher runs the daemon through a hidden VBScript that
// discards stderr, so daemon.log was never created there. Owning the file in
// the daemon makes logging work identically on every platform.
//
// Returns a cleanup func that closes the file; safe to call even when setup
// partially failed. Best-effort: a logging failure must never stop the daemon,
// so on error we leave log output at its default (stderr) and carry on.
func setupLogging(ctx context.Context) func() {
	path, err := config.LogFile()
	if err != nil {
		log.Printf("logging: cannot resolve daemon.log path: %v", err)
		return func() {}
	}
	if _, err := config.EnsureBlamelyDir(); err != nil {
		log.Printf("logging: cannot create ~/.blamely: %v", err)
		return func() {}
	}
	// O_RDWR (not O_WRONLY|O_APPEND): prune reads, truncates and rewrites the
	// file through THIS one handle with explicit Seeks. A second handle plus
	// O_APPEND+Truncate is unreliable on Windows (the rewrite could no-op,
	// leaving the file unpruned); one read-write handle behaves identically on
	// every platform.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		log.Printf("logging: cannot open %s: %v", path, err)
		return func() {}
	}
	rl := &rotatingLog{f: f, path: path, maxAge: logRetention}
	log.SetFlags(log.LstdFlags)
	log.SetOutput(rl)
	rl.prune() // trim anything already older than the window on startup
	go rl.prunePeriodically(ctx, time.Hour)
	return func() {
		rl.mu.Lock()
		_ = f.Close()
		rl.mu.Unlock()
	}
}

// rotatingLog is the io.Writer behind the standard logger. It serializes writes
// and the periodic prune through one mutex so the pruner can rewrite the file
// without racing concurrent log lines.
type rotatingLog struct {
	mu     sync.Mutex
	f      *os.File
	path   string
	maxAge time.Duration
}

func (r *rotatingLog) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Without O_APPEND we position writes ourselves: seek to EOF so log lines
	// always extend the file (and land correctly after a prune rewrite).
	if _, err := r.f.Seek(0, io.SeekEnd); err != nil {
		return 0, err
	}
	return r.f.Write(p)
}

// prune drops every line older than maxAge, rewriting the file IN PLACE
// (Truncate(0) + append) rather than via rename. In-place keeps the same inode,
// so it works when a file is locked against rename (Windows) and doesn't strand
// the macOS/Linux service's stderr fd that points at the same file.
func (r *rotatingLog) prune() {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Read the whole file through our own handle (seek to start first), so we
	// never open a second descriptor — the source of the Windows flakiness.
	if _, err := r.f.Seek(0, io.SeekStart); err != nil {
		return
	}
	data, err := io.ReadAll(r.f)
	if err != nil || len(data) == 0 {
		return
	}
	kept := pruneOldLines(data, time.Now().Add(-r.maxAge))
	if len(kept) == len(data) {
		return // nothing aged out — avoid a needless rewrite
	}
	if err := r.f.Truncate(0); err != nil {
		return
	}
	if _, err := r.f.Seek(0, io.SeekStart); err != nil {
		return
	}
	if _, err := r.f.Write(kept); err != nil {
		log.Printf("logging: prune rewrite failed: %v", err)
	}
}

func (r *rotatingLog) prunePeriodically(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.prune()
		}
	}
}

// pruneOldLines returns data with every line older than cutoff removed. A line's
// time is read from its leading log timestamp; a line without one (a panic stack
// frame, a wrapped message) inherits the age decision of the last timestamped
// line, so multi-line entries are kept or dropped as a unit. Leading untimed
// lines are kept (we can't prove they're old).
func pruneOldLines(data []byte, cutoff time.Time) []byte {
	lines := bytes.SplitAfter(data, []byte("\n"))
	out := make([]byte, 0, len(data))
	keep := true
	for _, ln := range lines {
		if len(ln) == 0 {
			continue
		}
		if ts, ok := parseLogTime(ln); ok {
			keep = !ts.Before(cutoff)
		}
		if keep {
			out = append(out, ln...)
		}
	}
	return out
}

func parseLogTime(ln []byte) (time.Time, bool) {
	if len(ln) < len(logTimeLayout) {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation(logTimeLayout, string(ln[:len(logTimeLayout)]), time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
