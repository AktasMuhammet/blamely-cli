//go:build !windows

package install

import (
	"os"
	"path/filepath"
	"syscall"
)

// killProcess terminates a specific PID on Unix. On Unix the daemon is normally
// already stopped synchronously by UninstallDaemonAgent (launchctl unload /
// systemctl --user disable --now); this is a backstop for a daemon started
// outside the service manager (e.g. a manual `blamely daemon`).
func killProcess(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Signal(syscall.SIGKILL)
}

// killOtherDaemonProcesses is a no-op on Unix: the daemon is stopped
// synchronously by UninstallDaemonAgent (launchctl unload / systemctl --user
// disable --now), and the PID-file kill in killRunningDaemon covers a daemon
// started outside the service manager. Windows needs the extra image-name sweep;
// Unix doesn't.
func killOtherDaemonProcesses() {}

// removeInstalledBinary deletes the stable binary, the listed runtime/data files,
// and the bin dir (recursively — it may hold a bundled sqlite3 or other install
// files). On Unix a running executable and open files can be
// unlinked immediately (the daemon is already stopped synchronously by
// UninstallDaemonAgent), so this is direct.
func removeInstalledBinary(p string, extraFiles []string, purgeRoot string) error {
	// --purge: wipe the whole ~/.blamely tree (config.json, exclude, everything).
	// The daemon was already killed synchronously, so nothing is held open.
	if purgeRoot != "" {
		return os.RemoveAll(purgeRoot)
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, f := range extraFiles {
		_ = os.Remove(f) // best-effort; unlink works even if still open
	}
	// Remove the whole bin directory, not just the binary: a bundled sqlite3 or
	// other install-time file can share it, and a plain os.Remove(dir) only
	// succeeds when the folder is empty. The bin dir is exclusively Blamely's;
	// ~/.blamely (config, exclude, a kept db) is the parent and is untouched.
	_ = os.RemoveAll(filepath.Dir(p)) // best-effort
	return nil
}
