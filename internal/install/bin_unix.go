//go:build !windows

package install

import (
	"os"
	"path/filepath"
)

// removeInstalledBinary deletes the stable binary, the listed runtime/data files,
// and the now-empty bin dir. On Unix a running executable and open files can be
// unlinked immediately (the daemon is already stopped synchronously by
// UninstallDaemonAgent), so this is direct.
func removeInstalledBinary(p string, extraFiles []string) error {
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, f := range extraFiles {
		_ = os.Remove(f) // best-effort; unlink works even if still open
	}
	_ = os.Remove(filepath.Dir(p)) // best-effort: only succeeds if empty
	return nil
}
