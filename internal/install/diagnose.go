package install

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/blamely/blamely/internal/config"
)

// diagnoseDaemon prints actionable recovery information when the daemon
// doesn't come up after `blamely install`. It tails the daemon log (if any),
// names the OS-specific manager, and prints concrete next-step commands the
// user can run themselves.
//
// Used only on the unhappy path: the install command has already retried via
// WaitForReady and given up. We assume the user wants to know exactly what
// to do next, not just "something went wrong".
func diagnoseDaemon(rootErr error, agentRef string) {
	fmt.Println()
	fmt.Println("  ⚠  Daemon health check failed — hooks may not be processed.")
	fmt.Printf("     reason: %v\n", rootErr)
	fmt.Println()

	// 1. Tail the log if we have one.
	if logPath, err := config.LogFile(); err == nil {
		if tail := tailFile(logPath, 15); tail != "" {
			fmt.Printf("  Recent log lines from %s:\n", logPath)
			for _, line := range strings.Split(strings.TrimRight(tail, "\n"), "\n") {
				fmt.Printf("    │ %s\n", line)
			}
			fmt.Println()
		} else {
			fmt.Printf("  Log file %s is empty or missing — the daemon may not have started at all.\n", logPath)
			fmt.Println()
		}
	}

	// 2. Recovery commands, tailored per OS.
	fmt.Println("  Try one of these:")
	fmt.Println("    • blamely status                — confirm whether the daemon is up")
	fmt.Println("    • blamely install               — re-run install (this regenerates the agent)")
	switch runtime.GOOS {
	case "darwin":
		fmt.Printf("    • launchctl list | grep blamely — check launchd state\n")
		fmt.Printf("    • launchctl unload %s\n", agentRef)
		fmt.Printf("      launchctl load   %s — reload the launch agent\n", agentRef)
	case "linux":
		fmt.Println("    • systemctl --user status blamely.service")
		fmt.Println("    • systemctl --user restart blamely.service")
		fmt.Println("    • journalctl --user -u blamely.service -n 50")
	case "windows":
		fmt.Println("    • schtasks /Query /TN \"Blamely Daemon\" /V")
		fmt.Println("    • schtasks /Run   /TN \"Blamely Daemon\"")
	}
	fmt.Println("    • Common causes:")
	fmt.Println("        - another blamely (or a stale daemon) is holding the port file")
	fmt.Println("        - the binary at the stable path was moved/deleted (re-run install)")
	fmt.Println("        - filesystem permission issue on ~/.blamely/")
	fmt.Println()
}

// tailFile returns the last `n` lines of path, or "" if the file is empty or
// can't be read. Reads the whole file (logs are small in practice); kept
// simple so install doesn't depend on a tail library.
func tailFile(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
