package daemon

import (
	"fmt"
	"net/http"
	"time"

	"github.com/blamely/blamely/internal/store"
)

// healthClient returns an HTTP client and target URL for the daemon's /health
// endpoint. It prefers the Unix domain socket (daemon.sock); if unavailable it
// falls back to the TCP port (daemon.port). The second return value is the
// human-readable address for status messages.
func healthClient() (*http.Client, string, string, error) {
	if sock, err := ReadSocket(); err == nil {
		c := UnixHTTPClient(sock)
		return c, "http://unix/health", sock, nil
	}
	port, err := ReadPort()
	if err != nil {
		return nil, "", "", err
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/health", port)
	c := &http.Client{Timeout: 2 * time.Second}
	return c, url, fmt.Sprintf("127.0.0.1:%d", port), nil
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
	client, url, addr, err := healthClient()
	if err != nil {
		fmt.Println("daemon: NOT RUNNING")
		fmt.Printf("  (could not read socket or port file: %v)\n", err)
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
