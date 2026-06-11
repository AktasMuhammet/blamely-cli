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

// createNoWindow (CREATE_NO_WINDOW) launches a console process with no console
// window at all — the reliable way to start the console-subsystem blamely.exe
// daemon without a cmd window flashing on screen. HideWindow alone is not
// enough for console apps.
const createNoWindow = 0x08000000

var (
	shell32           = syscall.NewLazyDLL("shell32.dll")
	procIsUserAnAdmin = shell32.NewProc("IsUserAnAdmin")
)

func InstallDaemonAgent(binaryPath string) (string, error) {
	// ONLOGON scheduled tasks require elevation on most Windows builds, even for
	// the current user. Non-admin installs go straight to the Startup folder.
	if !isWindowsAdmin() {
		return installStartupAgent(binaryPath)
	}
	if ref, err := installScheduledTask(binaryPath); err == nil {
		return ref, nil
	} else if isSchtasksAccessDenied(err) {
		return installStartupAgent(binaryPath)
	} else {
		return "", err
	}
}

func isWindowsAdmin() bool {
	r, _, _ := procIsUserAnAdmin.Call()
	return r != 0
}

// daemonLauncherVBSContent returns a VBScript that starts `blamely daemon` with
// a hidden window (WScript.Shell.Run intWindowStyle=0). Routing the launch
// through this keeps the console-subsystem daemon from flashing a cmd window at
// logon or when the Scheduled Task fires.
func daemonLauncherVBSContent(binaryPath string) string {
	return fmt.Sprintf(
		"Set WshShell = CreateObject(\"WScript.Shell\")\r\nWshShell.Run \"\"\"%s\"\" daemon\", 0, False\r\n",
		binaryPath,
	)
}

// launcherVBSPath is the stable launcher used by the Scheduled Task (kept in
// ~/.blamely/bin so it lives next to the binary, not in the Startup folder).
func launcherVBSPath() (string, error) {
	dir, err := config.BlamelyDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "bin", startupVBSName), nil
}

func installScheduledTask(binaryPath string) (string, error) {
	// A stale task from a prior install under a different security context
	// (e.g. an elevated session) can make /Create /F fail with "Access is
	// denied" because the current user doesn't own it. Clear it first —
	// best-effort, since a missing task also errors.
	_ = exec.Command("schtasks", "/Delete", "/F", "/TN", scheduledTaskName).Run()

	// Launch via a hidden VBScript so the ONLOGON trigger doesn't pop a console
	// window every login. wscript runs the .vbs, which starts the daemon hidden.
	vbs, err := launcherVBSPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(vbs), 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", filepath.Dir(vbs), err)
	}
	if err := atomicWrite(vbs, []byte(daemonLauncherVBSContent(binaryPath)), 0o644); err != nil {
		return "", err
	}

	// /F = force overwrite; /SC ONLOGON = run at every user logon; /RL LIMITED = user privileges.
	args := []string{
		"/Create", "/F",
		"/TN", scheduledTaskName,
		"/TR", fmt.Sprintf("wscript.exe //B //Nologo \"%s\"", vbs),
		"/SC", "ONLOGON",
		"/RL", "LIMITED",
	}
	if out, err := exec.Command("schtasks", args...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("schtasks /Create: %w: %s", err, strings.TrimSpace(string(out)))
	}
	// Kill any prior running instance so a reinstall picks up the new binary,
	// then start the daemon now (hidden, via CREATE_NO_WINDOW). Both best-effort.
	_ = exec.Command("schtasks", "/End", "/TN", scheduledTaskName).Run()
	_ = startDaemonNow(binaryPath)
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
	if err := atomicWrite(vbsPath, []byte(daemonLauncherVBSContent(binaryPath)), 0o644); err != nil {
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

// startDaemonNow launches the daemon for the current session with no console
// window (CREATE_NO_WINDOW). HideWindow is kept as a belt-and-suspenders hint.
func startDaemonNow(binaryPath string) error {
	cmd := exec.Command(binaryPath, "daemon")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release() // detach: the daemon outlives this installer
	return nil
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
	if vbs, err := launcherVBSPath(); err == nil {
		_ = os.Remove(vbs)
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
