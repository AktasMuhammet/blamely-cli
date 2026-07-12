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

// watchdogTaskName is a periodic Scheduled Task that re-launches the hidden
// daemon every couple of minutes. Windows has no launchd KeepAlive / systemd
// Restart=always equivalent, so the ONLOGON task (or Startup-folder entry)
// only revives the daemon at logon — a single mid-session crash would leave it
// dead until the next login. The daemon's single-instance guard
// (AnotherDaemonHealthy) makes a redundant launch a fast no-op, so firing this
// on a fixed interval simply resurrects the daemon whenever it isn't running.
const watchdogTaskName = "Blamely Daemon Watchdog"

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

// writeLauncherVBS (re)writes the stable hidden launcher next to the binary
// and returns its path.
func writeLauncherVBS(binaryPath string) (string, error) {
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
	return vbs, nil
}

// createOnLogonTask registers (or force-overwrites) the ONLOGON task that
// launches the daemon hidden at every user logon. Shared by the installer and
// the daemon's startup self-heal (EnsureDaemonAgent).
func createOnLogonTask(vbs string) error {
	// /F = force overwrite; /SC ONLOGON = run at every user logon; /RL LIMITED = user privileges.
	args := []string{
		"/Create", "/F",
		"/TN", scheduledTaskName,
		"/TR", fmt.Sprintf("wscript.exe //B //Nologo \"%s\"", vbs),
		"/SC", "ONLOGON",
		"/RL", "LIMITED",
	}
	if out, err := exec.Command("schtasks", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("schtasks /Create: %w: %s", err, strings.TrimSpace(string(out)))
	}
	allowBatteryStart(scheduledTaskName)
	return nil
}

func installScheduledTask(binaryPath string) (string, error) {
	// A stale task from a prior install under a different security context
	// (e.g. an elevated session) can make /Create /F fail with "Access is
	// denied" because the current user doesn't own it. Clear it first —
	// best-effort, since a missing task also errors.
	_ = exec.Command("schtasks", "/Delete", "/F", "/TN", scheduledTaskName).Run()

	// Launch via a hidden VBScript so the ONLOGON trigger doesn't pop a console
	// window every login. wscript runs the .vbs, which starts the daemon hidden.
	vbs, err := writeLauncherVBS(binaryPath)
	if err != nil {
		return "", err
	}
	if err := createOnLogonTask(vbs); err != nil {
		return "", err
	}
	// Register the periodic keepalive watchdog so a mid-session crash is revived
	// without waiting for the next logon. Best-effort: the ONLOGON task already
	// covers login, so a watchdog failure must not fail the whole install.
	_ = installWatchdogTask(vbs)

	// Kill any prior running instance so a reinstall picks up the new binary,
	// then start the daemon now (hidden, via CREATE_NO_WINDOW). Both best-effort.
	_ = exec.Command("schtasks", "/End", "/TN", scheduledTaskName).Run()
	_ = startDaemonNow(binaryPath)
	return scheduledTaskName, nil
}

// installWatchdogTask registers (idempotently) the periodic keepalive task that
// relaunches the daemon via the hidden VBS launcher every 2 minutes. A per-user
// time-triggered task does not require elevation (unlike ONLOGON), so this also
// gives the non-admin Startup-folder install crash recovery, not just logon
// recovery. Best-effort by contract: callers ignore the error.
func installWatchdogTask(vbs string) error {
	// Clear any stale task from a prior install under a different security
	// context; a missing task also errors, so this is best-effort.
	_ = exec.Command("schtasks", "/Delete", "/F", "/TN", watchdogTaskName).Run()

	// /SC MINUTE /MO 2 = fire every 2 minutes, indefinitely. The launched daemon
	// self-exits when another instance is already healthy, so this is a no-op
	// while the daemon is up and a resurrection once it has died.
	args := []string{
		"/Create", "/F",
		"/TN", watchdogTaskName,
		"/TR", fmt.Sprintf("wscript.exe //B //Nologo \"%s\"", vbs),
		"/SC", "MINUTE",
		"/MO", "2",
		"/RL", "LIMITED",
	}
	if out, err := exec.Command("schtasks", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("schtasks /Create watchdog: %w: %s", err, strings.TrimSpace(string(out)))
	}
	allowBatteryStart(watchdogTaskName)
	return nil
}

// allowBatteryStart patches a just-created Scheduled Task so it also fires on
// battery power. schtasks /Create has no switch for these settings, and its
// defaults (DisallowStartIfOnBatteries + StopIfGoingOnBatteries) silently keep
// the daemon dead on a laptop that boots unplugged: neither the ONLOGON revive
// nor the 2-minute watchdog ever fires, so after a shutdown → power-on cycle
// the daemon stays down until the editor plugin's /health respawn kicks in.
// -StartWhenAvailable additionally lets the watchdog fire promptly after boot
// instead of waiting for the next interval boundary. Best-effort: an old
// PowerShell or a policy-restricted box keeps the schtasks defaults, which is
// exactly the pre-patch behavior.
func allowBatteryStart(taskName string) {
	_ = exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		fmt.Sprintf(
			"Set-ScheduledTask -TaskName '%s' -Settings (New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable)",
			taskName,
		)).Run()
}

func installStartupAgent(binaryPath string) (string, error) {
	vbsPath, err := installStartupAgentEntry(binaryPath)
	if err != nil {
		return "", err
	}
	// Crash recovery between logons: a per-user time-triggered task needs no
	// elevation, so even this non-admin path gets keepalive. Best-effort — the
	// Startup entry still covers the next logon if the watchdog can't register.
	_ = installWatchdogTask(vbsPath)
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

// taskExists reports whether a Scheduled Task with the given name is registered.
func taskExists(taskName string) bool {
	return exec.Command("schtasks", "/Query", "/TN", taskName).Run() == nil
}

// EnsureDaemonAgent is the daemon's startup self-heal: it re-asserts the
// autostart registration WITHOUT touching the already-running daemon (no
// /End, no immediate relaunch — installScheduledTask does those and would
// kill the caller). Called best-effort every time the daemon starts, however
// it was started (logon task, watchdog, editor-plugin respawn, or by hand) —
// so a machine whose tasks were never created, were removed, or predate the
// battery-settings fix converges to a working autostart the first time any
// daemon comes up.
func EnsureDaemonAgent(binaryPath string) error {
	vbs, err := writeLauncherVBS(binaryPath)
	if err != nil {
		return err
	}
	if taskExists(scheduledTaskName) {
		// Re-apply the battery settings: tasks created before allowBatteryStart
		// existed never fire on a laptop booting unplugged.
		allowBatteryStart(scheduledTaskName)
	} else if isWindowsAdmin() {
		if err := createOnLogonTask(vbs); err != nil {
			// Fall back like InstallDaemonAgent does: a Startup-folder entry
			// needs no elevation and still covers the next logon.
			if _, serr := installStartupAgentEntry(binaryPath); serr != nil {
				return serr
			}
		}
	} else {
		if _, err := installStartupAgentEntry(binaryPath); err != nil {
			return err
		}
	}
	// The watchdog is a per-user time trigger — no elevation needed, so ensure
	// it on every path. installWatchdogTask is idempotent (/Delete + /Create /F)
	// and re-applies the battery settings itself.
	return installWatchdogTask(vbs)
}

// installStartupAgentEntry writes only the Startup-folder VBS (the non-admin
// logon revive), without launching the daemon — shared by installStartupAgent
// (which also starts the daemon now) and EnsureDaemonAgent (where the daemon
// is already running).
func installStartupAgentEntry(binaryPath string) (string, error) {
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
	return vbsPath, nil
}

func UninstallDaemonAgent() error {
	if startupDir, err := windowsStartupDir(); err == nil {
		_ = os.Remove(filepath.Join(startupDir, startupVBSName))
	}
	if vbs, err := launcherVBSPath(); err == nil {
		_ = os.Remove(vbs)
	}
	// Remove the periodic keepalive watchdog. Best-effort: it won't exist on
	// installs that predate it, and a missing task makes /Delete error.
	_ = exec.Command("schtasks", "/Delete", "/F", "/TN", watchdogTaskName).Run()
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
