//go:build windows

package install

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/blamely/blamely/internal/config"
)

const scheduledTaskName = "Blamely Daemon"

const startupVBSName = "blamely-daemon.vbs"

func InstallDaemonAgent(binaryPath string) (string, error) {
	if ref, err := installScheduledTask(binaryPath); err == nil {
		return ref, nil
	} else if !isSchtasksAccessDenied(err) {
		return "", err
	}
	// ONLOGON scheduled tasks often need an elevated shell on Windows, even
	// when the task runs as the current user. Fall back to a per-user Startup
	// shortcut — no admin rights required.
	ref, err := installStartupAgent(binaryPath)
	if err != nil {
		return "", fmt.Errorf("%w\n\n  hint: run PowerShell as Administrator and re-run `blamely install`,\n  or open Task Scheduler, delete any \"Blamely Daemon\" task, then retry", err)
	}
	return ref, nil
}

func installScheduledTask(binaryPath string) (string, error) {
	// A stale task from a prior install under a different security context
	// (e.g. an elevated session) can make /Create /F fail with "Access is
	// denied" because the current user doesn't own it. Clear it first —
	// best-effort, since a missing task also errors.
	_ = exec.Command("schtasks", "/Delete", "/F", "/TN", scheduledTaskName).Run()

	// /F = force overwrite; /SC ONLOGON = run at every user logon; /RL LIMITED = user privileges.
	args := []string{
		"/Create", "/F",
		"/TN", scheduledTaskName,
		"/TR", fmt.Sprintf("\"%s\" daemon", binaryPath),
		"/SC", "ONLOGON",
		"/RL", "LIMITED",
	}
	if out, err := exec.Command("schtasks", args...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("schtasks /Create: %w: %s", err, strings.TrimSpace(string(out)))
	}
	// Kill any prior running instance so a reinstall picks up the new
	// binary; then start fresh. Both are best-effort.
	_ = exec.Command("schtasks", "/End", "/TN", scheduledTaskName).Run()
	_ = exec.Command("schtasks", "/Run", "/TN", scheduledTaskName).Run()
	return scheduledTaskName, nil
}

func installStartupAgent(binaryPath string) (string, error) {
	startupDir, err := windowsStartupDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(startupDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir startup: %w", err)
	}
	vbsPath := filepath.Join(startupDir, startupVBSName)
	// Run the daemon hidden at logon (window style 0). /B isn't available from VBS.
	script := fmt.Sprintf(
		"Set WshShell = CreateObject(\"WScript.Shell\")\r\nWshShell.Run \"\"\"%s\"\" daemon\", 0, False\r\n",
		binaryPath,
	)
	if err := atomicWrite(vbsPath, []byte(script), 0o644); err != nil {
		return "", err
	}
	if err := startDaemonNow(binaryPath); err != nil {
		return vbsPath, fmt.Errorf("startup entry written but daemon failed to start: %w", err)
	}
	return vbsPath, nil
}

func windowsStartupDir() (string, error) {
	home, err := config.Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "AppData", "Roaming", "Microsoft", "Windows", "Start Menu", "Programs", "Startup"), nil
}

func startDaemonNow(binaryPath string) error {
	cmd := exec.Command(binaryPath, "daemon")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Start()
}

func isSchtasksAccessDenied(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "access is denied") ||
		strings.Contains(msg, "erişim engellendi") ||
		strings.Contains(msg, "access denied")
}

func UninstallDaemonAgent() error {
	if startupDir, err := windowsStartupDir(); err == nil {
		_ = os.Remove(filepath.Join(startupDir, startupVBSName))
	}
	err := exec.Command("schtasks", "/Delete", "/F", "/TN", scheduledTaskName).Run()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil
		}
		return err
	}
	return nil
}
