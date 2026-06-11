package daemon

import (
	"fmt"
	"net/http"
	"time"
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
	return nil
}
