package daemon

import (
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"time"

	"github.com/blamely/blamely/internal/config"
	"github.com/blamely/blamely/internal/store"
	"github.com/blamely/blamely/internal/updatehint"
)

// healthClient returns an HTTP client and target URL for the daemon's /health
// endpoint. It prefers the Unix domain socket (daemon.sock); if unavailable it
// falls back to the TCP port (daemon.port). The second return value is the
// human-readable address for status messages.
func healthClient() (*http.Client, string, string, error) {
	sock, sockErr := ReadSocket()
	if sockErr == nil {
		c := UnixHTTPClient(sock)
		return c, "http://unix/health", sock, nil
	}
	port, portErr := ReadPort()
	if portErr != nil {
		// Neither discovery file exists → the daemon isn't running. Report BOTH
		// mechanisms rather than just the port error: on macOS/Linux the daemon
		// binds a socket and never writes a port file, so a bare "read port file
		// …daemon.port: no such file" reason is actively misleading (it names a
		// file that platform never uses). Naming the socket path too tells the
		// operator what actually should have appeared.
		return nil, "", "", noDaemonError(sockErr, portErr)
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/health", port)
	c := &http.Client{Timeout: 2 * time.Second}
	return c, url, fmt.Sprintf("127.0.0.1:%d", port), nil
}

// noDaemonError builds the "daemon not running" error shown when neither the
// socket nor the port file exists. It names the paths the daemon would have
// created so the reason is truthful on every platform (socket-based on
// macOS/Linux, port-based on Windows). Falls back to the underlying errors if a
// path can't be resolved.
func noDaemonError(sockErr, portErr error) error {
	sockPath, sperr := config.SocketFile()
	portPath, pperr := config.PortFile()
	if sperr != nil || pperr != nil {
		return fmt.Errorf("daemon not running: %w", errors.Join(sockErr, portErr))
	}
	if runtime.GOOS == "windows" {
		// Windows is forced onto TCP (see server.go), so the port file is the
		// real indicator there; mention the socket second.
		return fmt.Errorf("daemon not running: no port file at %s (and no socket at %s)", portPath, sockPath)
	}
	return fmt.Errorf("daemon not running: no socket at %s (and no port file at %s)", sockPath, portPath)
}

// AnotherDaemonHealthy does ONE quick /health probe of an already-running daemon.
// Used at startup as a single-instance guard: the editor plugins spawn a daemon
// whenever a /health ping fails, so redundant spawns can accumulate — each running
// every watcher and writing the SAME SQLite DB (extra CPU + DB contention). The
// daemon binds an ephemeral TCP port on Windows, so the OS won't reject the duplicate;
// this catches the common case where a healthy daemon is already up. Returns false on
// any error (no port/socket file, or the existing daemon doesn't answer).
func AnotherDaemonHealthy() bool {
	client, url, _, err := healthClient()
	if err != nil {
		return false
	}
	client.Timeout = 700 * time.Millisecond
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// WaitForReady blocks until the daemon answers /health with 200, or timeout
// elapses. Tries the Unix socket first, falls back to TCP port. Returns the
// listening address string ("~/.blamely/daemon.sock" or "127.0.0.1:PORT").
func WaitForReady(timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		client, url, addr, err := healthClient()
		if err != nil {
			lastErr = err
			time.Sleep(200 * time.Millisecond)
			continue
		}
		client.Timeout = 500 * time.Millisecond
		resp, herr := client.Get(url)
		if herr != nil {
			lastErr = herr
			time.Sleep(200 * time.Millisecond)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return addr, nil
		}
		lastErr = fmt.Errorf("health: status %d", resp.StatusCode)
		time.Sleep(200 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("timeout after %s", timeout)
	}
	return "", lastErr
}

func PrintStatus() error {
	// Printed first, and independently of daemon health: whether a newer blamely
	// exists has nothing to do with whether the daemon is up, and the paths
	// below return early.
	printUpdateHint()
	client, url, addr, err := healthClient()
	if err != nil {
		fmt.Println("daemon: NOT RUNNING")
		fmt.Printf("  (%v)\n", err)
		return nil
	}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Printf("daemon: FILE PRESENT but health check failed: %v\n", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Printf("daemon: UNHEALTHY (status %d)\n", resp.StatusCode)
		return nil
	}
	fmt.Printf("daemon: HEALTHY on %s\n", addr)
	printSessionUsage()
	return nil
}

// printUpdateHint shows what the daemon's periodic check last found. It reads
// the recorded hint only — status never makes a network call, so it stays
// instant and silent offline.
func printUpdateHint() {
	h, ok := updatehint.Read()
	if !ok {
		return
	}
	fmt.Printf("update:  %s available (run `blamely update`)\n", h.Version)
}

// printSessionUsage shows recent per-session, per-model CUMULATIVE token totals
// (e.g. the Copilot CLI's session.shutdown metrics). Best-effort: a missing/locked
// DB or empty table just prints nothing. These are session-level — they cover a
// whole session's spend and are NOT the same as per-edit/commit attribution.
func printSessionUsage() {
	db, err := store.Open()
	if err != nil {
		return
	}
	defer db.Close()
	rows, err := db.RecentSessionUsage(10)
	if err != nil || len(rows) == 0 {
		return
	}
	fmt.Println("\nSession token usage (live, cumulative per session — not per-edit):")
	for _, r := range rows {
		u := r.Usage
		fmt.Printf("  %-8s  %-8s  %-14s  in %s · out %s · cache_r %s · cache_w %s · reason %s\n",
			shortID(r.SessionID), r.Tool, r.Model,
			compactNum(u.InputTokens), compactNum(u.OutputTokens),
			compactNum(u.CacheReadTokens), compactNum(u.CacheWriteTokens), compactNum(u.ReasoningTokens))
	}
}

func shortID(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// compactNum renders a token count as e.g. 73.3k / 1.4k / 418.
func compactNum(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}
