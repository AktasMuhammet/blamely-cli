//go:build !windows

package install

import (
	"os"
	"path/filepath"
)

// removeInstalledBinary deletes the stable binary and its now-empty bin dir.
// On Unix a running executable can be unlinked immediately, so this is direct.
func removeInstalledBinary(p string) error {
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = os.Remove(filepath.Dir(p)) // best-effort: only succeeds if empty
	return nil
}
