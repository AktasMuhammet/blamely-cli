package daemon

import (
	"fmt"
	"net/http"
	"time"
)

// WaitForReady blocks until the daemon's port file exists AND a /health
// request returns 200, or `timeout` elapses. Used by `blamely install` to
// confirm the launchd/systemd-managed daemon actually came up after the agent
// was registered. Returns (port, nil) on success and (0, error) on timeout.
func WaitForReady(timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	var lastErr error
	for time.Now().Before(deadline) {
		port, err := ReadPort()
		if err != nil {
			lastErr = err
			time.Sleep(200 * time.Millisecond)
			continue
		}
		resp, herr := client.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
		if herr != nil {
			lastErr = herr
			time.Sleep(200 * time.Millisecond)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return port, nil
		}
		lastErr = fmt.Errorf("health: status %d", resp.StatusCode)
		time.Sleep(200 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("timeout after %s", timeout)
	}
	return 0, lastErr
}

func PrintStatus() error {
	port, err := ReadPort()
	if err != nil {
		fmt.Println("daemon: NOT RUNNING")
		fmt.Printf("  (could not read port file: %v)\n", err)
		return nil
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/health", port)
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Printf("daemon: PORT FILE PRESENT but health check failed: %v\n", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Printf("daemon: UNHEALTHY (status %d)\n", resp.StatusCode)
		return nil
	}
	fmt.Printf("daemon: HEALTHY on 127.0.0.1:%d\n", port)
	return nil
}
