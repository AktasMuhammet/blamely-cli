package tools

// Debug logging for the Claude Code hook path.
//
// `blamely record claude` runs INSIDE Claude Code as a PostToolUse hook, so
// everything it writes to stdout/stderr is swallowed by the host — and the
// daemon never sees a payload that was rejected, mis-parsed, or dropped before
// the POST. That leaves the most failure-prone stretch of the attribution
// pipeline with no trace at all.
//
// This file gives that stretch its own append-only log at
// ~/.blamely/claude-debug.log, which `blamely log claude --debug` reads and
// follows. Every write is best-effort and error-swallowing: cmdRecord's
// contract is "always exit 0, never break the host tool", and a debug logger
// must not be the thing that violates it.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blamely/blamely/internal/config"
	"github.com/blamely/blamely/internal/daemon"
)

const (
	// claudeDebugLogName is the log file inside ~/.blamely.
	claudeDebugLogName = "claude-debug.log"
	// claudeDebugRotatedSuffix is appended to the previous generation when the
	// live log is rotated.
	claudeDebugRotatedSuffix = ".1"
	// claudeDebugMaxBytes caps the live log before it is rotated. The hook
	// fires on every AI edit, so an uncapped log would grow without bound.
	claudeDebugMaxBytes = 4 << 20
	// claudeDebugMaxField truncates one logged value, so a single Write of a
	// large file can't drown out the surrounding steps.
	claudeDebugMaxField = 2000
	// claudeDebugEnvVar disables logging when set to a false-y value.
	claudeDebugEnvVar = "BLAMELY_DEBUG_CLAUDE"
	// claudeDebugTimeLayout keeps millisecond precision: several hook steps
	// routinely land inside the same second, and their order is the point.
	claudeDebugTimeLayout = "2006-01-02T15:04:05.000Z07:00"
)

// claudeDebugMu serialises writes from goroutines inside one hook process.
// Concurrent hook PROCESSES are not serialised — each line is written with a
// single append-mode Write call, which the OS keeps intact, and every line
// carries its own invocation id so interleaved runs stay readable.
var claudeDebugMu sync.Mutex

// ClaudeDebugLogPath returns the path of the Claude hook debug log
// (~/.blamely/claude-debug.log).
func ClaudeDebugLogPath() (string, error) {
	dir, err := config.BlamelyDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, claudeDebugLogName), nil
}

// claudeDebugEnabled reports whether the hook path should write its debug log.
// Logging is ON by default — the whole point is that a user who hits a missing
// attribution can inspect what the hook did without first reproducing it under
// a flag. Set BLAMELY_DEBUG_CLAUDE=0 (or false/off/no) to silence it.
func claudeDebugEnabled() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(claudeDebugEnvVar)))
	switch v {
	case "0", "false", "off", "no":
		return false
	default:
		return true
	}
}

// claudeDebug is the per-invocation logger. One hook run = one instance, so
// every line it writes shares an id and the lines of a single Claude edit can
// be read as a group even when several hooks fire at once.
type claudeDebug struct {
	id  string
	on  bool
	t0  time.Time
	log string // resolved log path; empty when unresolvable
}

// newClaudeDebug builds the logger for one `blamely record claude` invocation.
// It never returns nil: when logging is off or the path can't be resolved, the
// returned logger is inert and every method is a no-op.
func newClaudeDebug() *claudeDebug {
	d := &claudeDebug{t0: time.Now(), on: claudeDebugEnabled()}
	if !d.on {
		return d
	}
	path, err := ClaudeDebugLogPath()
	if err != nil {
		d.on = false
		return d
	}
	d.log = path
	// pid alone is not unique enough: hooks are short-lived, so the OS recycles
	// pids quickly. Pairing it with the start time's sub-second part keeps two
	// runs apart in the log without pulling in a random source.
	d.id = fmt.Sprintf("%d-%03d", os.Getpid(), d.t0.Nanosecond()/int(time.Millisecond))
	return d
}

// logf appends one line: <rfc3339 ts> [<id>] +<ms since start> <step> <detail>.
// step is a short stable keyword (payload, parse, resolve, post, …) so the log
// can be grepped by stage.
func (d *claudeDebug) logf(step, format string, args ...any) {
	if d == nil || !d.on {
		return
	}
	line := fmt.Sprintf("%s [%s] +%-6s %-14s %s\n",
		time.Now().Format(claudeDebugTimeLayout), d.id,
		time.Since(d.t0).Truncate(time.Millisecond).String(),
		step, fmt.Sprintf(format, args...))
	d.append(line)
}

// append writes one already-formatted line, rotating first if the log has
// outgrown claudeDebugMaxBytes. All errors are swallowed: a hook must never
// fail because its debug log couldn't be written.
func (d *claudeDebug) append(line string) {
	claudeDebugMu.Lock()
	defer claudeDebugMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(d.log), 0o755); err != nil {
		return
	}
	rotateClaudeDebugLog(d.log)
	f, err := os.OpenFile(d.log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line)
}

// rotateClaudeDebugLog moves the live log aside to <name>.1 once it exceeds the
// size cap, keeping exactly one previous generation. Best-effort.
func rotateClaudeDebugLog(path string) {
	fi, err := os.Stat(path)
	if err != nil || fi.Size() < claudeDebugMaxBytes {
		return
	}
	_ = os.Remove(path + claudeDebugRotatedSuffix)
	_ = os.Rename(path, path+claudeDebugRotatedSuffix)
}

// debugField renders one value for the log: whitespace-collapsed, truncated to
// claudeDebugMaxField, and quoted so an empty or space-bearing value is still
// visible as such.
func debugField(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > claudeDebugMaxField {
		s = s[:claudeDebugMaxField] + "…(truncated)"
	}
	return strconv.Quote(s)
}

// daemonEndpoint describes how postToDaemon would reach the daemon right now,
// and whether it can at all. Worth logging on its own because postToDaemon
// returns nil — not an error — when no daemon is running: the edit is silently
// dropped, which is the single most common reason an attribution never appears.
// Without this the post step would report "ok" for an edit that went nowhere.
func daemonEndpoint() (desc string, reachable bool) {
	if sock, err := daemon.ReadSocket(); err == nil {
		return "unix:" + sock, true
	}
	if port, err := daemon.ReadPort(); err == nil {
		return fmt.Sprintf("tcp:127.0.0.1:%d", port), true
	}
	return "UNREACHABLE (daemon not running)", false
}

// daemonEndpointDesc is daemonEndpoint's description alone, for the log lines
// that only need to name the endpoint.
func daemonEndpointDesc() string {
	desc, _ := daemonEndpoint()
	return desc
}

// claudeTranscriptSearchDesc explains where a transcript path WOULD have been
// found, for the case where derivation came back empty. No transcript means no
// model, no token counts and a defaulted gen_type, so the miss needs to be
// visible rather than logged as an empty string.
func claudeTranscriptSearchDesc(cwd, sessionID string) string {
	if cwd == "" || sessionID == "" {
		return fmt.Sprintf("cannot derive (cwd=%s session=%s)", debugField(cwd), debugField(sessionID))
	}
	proj := strings.ReplaceAll(filepath.ToSlash(cwd), "/", "-")
	bases := config.ClaudeProjectsDirs()
	searched := make([]string, 0, len(bases))
	for _, b := range bases {
		searched = append(searched, abbreviateHome(filepath.Join(b, proj, sessionID+".jsonl")))
	}
	return "NOT FOUND, searched: " + strings.Join(searched, ", ")
}

// debugHost names the editor a payload came from, for the log's host= field.
func debugHost(isCursor bool) string {
	if isCursor {
		return "cursor"
	}
	return "claude"
}

// debugTokens renders an optional token count: "-" when the transcript carried
// no usage for this edit, which is itself worth seeing in the log.
func debugTokens(n *int64) string {
	if n == nil {
		return "-"
	}
	return strconv.FormatInt(*n, 10)
}

// debugFileExists reports whether path is present, for logging a derived
// transcript path that may point at nothing.
func debugFileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}
