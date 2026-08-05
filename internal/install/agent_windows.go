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

// keepaliveTaskName is a periodic Scheduled Task that re-launches the daemon if
// it has died. Windows has no launchd KeepAlive / systemd Restart=always
// equivalent, so the ONLOGON task only revives the daemon at logon — a
// mid-session crash would otherwise leave it dead until the next login. The
// daemon's single-instance guard (AnotherDaemonHealthy) makes a redundant
// launch a fast no-op, so firing this on a timer simply resurrects the daemon
// whenever it isn't running.
const keepaliveTaskName = "Blamely Daemon Keepalive"

// keepaliveMinutes is how often the keepalive task fires.
//
// This was 2 minutes. A signed binary relaunched every 2 minutes from a
// Scheduled Task is, behaviourally, indistinguishable from malware refusing to
// stay killed — it was part of what tripped Defender (see the file comment on
// startupShortcutName). 15 minutes still recovers a crashed daemon without a
// human noticing, and it is not the only recovery path: the editor plugins
// respawn the daemon when their /health probe fails, and EnsureDaemonAgent
// self-heals on every daemon start.
const keepaliveMinutes = 15

// startupShortcutName is the non-admin autostart fallback: a plain .lnk pointing
// at the signed blamely.exe.
//
// It replaces blamely-daemon.vbs. That VBScript, launched via
// `wscript.exe //B //Nologo` from both the Startup folder and a 2-minute
// Scheduled Task, was reported by Windows Defender as
// Trojan:Win32/Commando.A!ml — a machine-learning verdict on the SHAPE of the
// install, not on any specific code:
//
//	unsigned script + LOLBin script host + Startup-folder persistence +
//	a second scheduled-task persistence + a 2-minute relaunch loop + hidden window
//
// Each is ordinary alone; together they are a textbook persistence template.
// Worse, routing through wscript threw away the very thing that should make
// blamely trustworthy on Windows: the binary is Authenticode-signed by a
// verified publisher, but Defender never saw that signature on the thing that
// actually started at boot, because the thing that started was an unsigned .vbs.
//
// Everything here now launches the signed blamely.exe directly. The console
// window that used to justify the VBScript is handled inside the binary
// (`daemon --background`, see cmd/blamely/console_windows.go).
const startupShortcutName = "Blamely.lnk"

// Legacy artifact names, removed on install/ensure/uninstall so machines that
// already have the flagged VBScript layout converge to the clean one. Without
// this, an upgrade would leave the .vbs on disk and the old task registered —
// still detected, and now orphaned.
const (
	legacyStartupVBSName   = "blamely-daemon.vbs"
	legacyWatchdogTaskName = "Blamely Daemon Watchdog"
)

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
	removeLegacyAgentArtifacts()

	// ONLOGON scheduled tasks require elevation on most Windows builds, even for
	// the current user. Non-admin installs fall back to the keepalive task (a
	// per-user time trigger needs no elevation) and then to a Startup shortcut.
	if isWindowsAdmin() {
		if ref, err := installScheduledTask(binaryPath); err == nil {
			return ref, nil
		} else if !isSchtasksAccessDenied(err) {
			return "", err
		}
	}
	return installUnprivilegedAgent(binaryPath)
}

func isWindowsAdmin() bool {
	r, _, _ := procIsUserAnAdmin.Call()
	return r != 0
}

// daemonCommand is the command line every autostart entry runs: the signed
// binary, with the flag that makes it drop the launcher's console window.
func daemonCommand(binaryPath string) string {
	return fmt.Sprintf("\"%s\" daemon --background", binaryPath)
}

// createOnLogonTask registers (or force-overwrites) the ONLOGON task that
// launches the daemon at every user logon. Shared by the installer and the
// daemon's startup self-heal (EnsureDaemonAgent).
func createOnLogonTask(binaryPath string) error {
	// /F = force overwrite; /SC ONLOGON = run at every user logon; /RL LIMITED = user privileges.
	args := []string{
		"/Create", "/F",
		"/TN", scheduledTaskName,
		"/TR", daemonCommand(binaryPath),
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

	if err := createOnLogonTask(binaryPath); err != nil {
		return "", err
	}
	// Register the periodic keepalive so a mid-session crash is revived without
	// waiting for the next logon. Best-effort: the ONLOGON task already covers
	// login, so a keepalive failure must not fail the whole install.
	_ = installKeepaliveTask(binaryPath)

	// Kill any prior running instance so a reinstall picks up the new binary,
	// then start the daemon now (hidden, via CREATE_NO_WINDOW). Both best-effort.
	_ = exec.Command("schtasks", "/End", "/TN", scheduledTaskName).Run()
	_ = startDaemonNow(binaryPath)
	return scheduledTaskName, nil
}

// installUnprivilegedAgent is the no-elevation path. A per-user time-triggered
// task needs no admin rights and fires after logon too (StartWhenAvailable), so
// it is tried FIRST and is normally the only autostart artifact this path
// creates — one mechanism, not two. The Startup shortcut is a fallback for
// machines where policy blocks task creation entirely.
func installUnprivilegedAgent(binaryPath string) (string, error) {
	if err := installKeepaliveTask(binaryPath); err == nil {
		_ = startDaemonNow(binaryPath)
		return keepaliveTaskName, nil
	}
	lnk, err := installStartupAgentEntry(binaryPath)
	if err != nil {
		return "", err
	}
	if err := startDaemonNow(binaryPath); err != nil {
		return lnk, fmt.Errorf("startup entry written but daemon failed to start: %w", err)
	}
	return lnk, nil
}

// installKeepaliveTask registers (idempotently) the periodic task that
// relaunches the daemon if it has died. A per-user time-triggered task does not
// require elevation (unlike ONLOGON), so this also gives the non-admin install
// crash recovery, not just logon recovery.
func installKeepaliveTask(binaryPath string) error {
	// Clear any stale task from a prior install under a different security
	// context; a missing task also errors, so this is best-effort.
	_ = exec.Command("schtasks", "/Delete", "/F", "/TN", keepaliveTaskName).Run()

	args := []string{
		"/Create", "/F",
		"/TN", keepaliveTaskName,
		"/TR", daemonCommand(binaryPath),
		"/SC", "MINUTE",
		"/MO", fmt.Sprint(keepaliveMinutes),
		"/RL", "LIMITED",
	}
	if out, err := exec.Command("schtasks", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("schtasks /Create keepalive: %w: %s", err, strings.TrimSpace(string(out)))
	}
	allowBatteryStart(keepaliveTaskName)
	return nil
}

// allowBatteryStart patches a just-created Scheduled Task so it also fires on
// battery power. schtasks /Create has no switch for these settings, and its
// defaults (DisallowStartIfOnBatteries + StopIfGoingOnBatteries) silently keep
// the daemon dead on a laptop that boots unplugged: neither the ONLOGON revive
// nor the keepalive ever fires, so after a shutdown → power-on cycle the daemon
// stays down until the editor plugin's /health respawn kicks in.
// -StartWhenAvailable additionally lets the keepalive fire promptly after boot
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
// it was started (logon task, keepalive, editor-plugin respawn, or by hand) —
// so a machine whose tasks were never created, were removed, or predate the
// battery-settings fix converges to a working autostart the first time any
// daemon comes up.
//
// This is also what migrates an existing install off the Defender-flagged
// VBScript layout: the legacy artifacts are removed and the tasks re-created to
// run the signed binary directly, without waiting for the user to reinstall.
func EnsureDaemonAgent(binaryPath string) error {
	removeLegacyAgentArtifacts()

	if taskExists(scheduledTaskName) {
		// Re-assert the command line (an install predating this change still
		// points at wscript + the .vbs we just deleted) and the battery
		// settings (tasks created before allowBatteryStart existed never fire
		// on a laptop booting unplugged).
		if isWindowsAdmin() {
			_ = createOnLogonTask(binaryPath)
		} else {
			allowBatteryStart(scheduledTaskName)
		}
	} else if isWindowsAdmin() {
		if err := createOnLogonTask(binaryPath); err != nil {
			// Fall back like InstallDaemonAgent does: the keepalive task (below)
			// needs no elevation, and a Startup shortcut covers the next logon.
			if _, serr := installStartupAgentEntry(binaryPath); serr != nil {
				return serr
			}
		}
	}
	// The keepalive is a per-user time trigger — no elevation needed, so ensure
	// it on every path. installKeepaliveTask is idempotent (/Delete + /Create /F)
	// and re-applies the battery settings itself.
	if err := installKeepaliveTask(binaryPath); err != nil {
		if taskExists(scheduledTaskName) {
			return nil // logon coverage is registered; keepalive is a bonus
		}
		if _, serr := installStartupAgentEntry(binaryPath); serr != nil {
			return serr
		}
	}
	return nil
}

// installStartupAgentEntry writes only the Startup-folder shortcut (the
// last-resort logon revive), without launching the daemon.
func installStartupAgentEntry(binaryPath string) (string, error) {
	startupDir, err := windowsStartupDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(startupDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir startup: %w", err)
	}
	lnk := filepath.Join(startupDir, startupShortcutName)
	if err := writeShortcut(lnk, binaryPath, "daemon --background"); err != nil {
		return "", err
	}
	return lnk, nil
}

// writeShortcut creates a .lnk pointing at target. A shortcut is what an
// ordinary application puts in the Startup folder, and it carries no code of its
// own — unlike the .vbs it replaces, what autostarts is the signed binary
// itself.
//
// COM (IShellLink) is driven through a one-shot PowerShell call rather than
// linking an OLE binding into the CLI: nothing is left on disk but the .lnk.
func writeShortcut(lnkPath, target, args string) error {
	script := fmt.Sprintf(
		"$s=(New-Object -ComObject WScript.Shell).CreateShortcut('%s');"+
			"$s.TargetPath='%s';$s.Arguments='%s';$s.WorkingDirectory='%s';"+
			"$s.WindowStyle=7;$s.Description='Blamely background service';$s.Save()",
		lnkPath, target, args, filepath.Dir(target),
	)
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("create startup shortcut: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if _, statErr := os.Stat(lnkPath); statErr != nil {
		return fmt.Errorf("create startup shortcut: %s was not written", lnkPath)
	}
	return nil
}

// removeLegacyAgentArtifacts deletes the pre-1.8.0 VBScript launchers and the
// task that ran them. Best-effort and idempotent: a clean machine has none of
// these, and a missing file/task is not an error worth surfacing.
func removeLegacyAgentArtifacts() {
	if startupDir, err := windowsStartupDir(); err == nil {
		_ = os.Remove(filepath.Join(startupDir, legacyStartupVBSName))
	}
	if dir, err := config.BlamelyDir(); err == nil {
		_ = os.Remove(filepath.Join(dir, "bin", legacyStartupVBSName))
	}
	_ = exec.Command("schtasks", "/Delete", "/F", "/TN", legacyWatchdogTaskName).Run()
}

func UninstallDaemonAgent() error {
	removeLegacyAgentArtifacts()
	if startupDir, err := windowsStartupDir(); err == nil {
		_ = os.Remove(filepath.Join(startupDir, startupShortcutName))
	}
	// Remove the periodic keepalive. Best-effort: it won't exist on installs
	// that predate it, and a missing task makes /Delete error.
	_ = exec.Command("schtasks", "/Delete", "/F", "/TN", keepaliveTaskName).Run()
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
