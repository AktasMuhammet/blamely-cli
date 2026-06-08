//go:build !windows

package install

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// InstallPathEntry on Unix writes a marker block to the user's shell rc.
func InstallPathEntry() (rcPath string, added bool, err error) {
	rcPath, kind, err := detectShellRC()
	if err != nil {
		return "", false, err
	}

	block, err := renderPathBlock(kind)
	if err != nil {
		return rcPath, false, err
	}

	current, err := readFileTolerant(rcPath)
	if err != nil {
		return rcPath, false, err
	}
	if strings.Contains(current, pathBlockStart) {
		return rcPath, false, nil
	}

	var sep string
	if len(current) > 0 && !strings.HasSuffix(current, "\n") {
		sep = "\n"
	}
	updated := current + sep + block
	if err := os.MkdirAll(filepath.Dir(rcPath), 0o755); err != nil {
		return rcPath, false, fmt.Errorf("mkdir %s: %w", filepath.Dir(rcPath), err)
	}
	if err := atomicWrite(rcPath, []byte(updated), 0o644); err != nil {
		return rcPath, false, err
	}
	return rcPath, true, nil
}

// UninstallPathEntry on Unix removes the marker block from the shell rc.
func UninstallPathEntry(rcPath string) (string, bool, error) {
	if rcPath == "" {
		p, _, err := detectShellRC()
		if err != nil {
			return "", false, err
		}
		rcPath = p
	}
	current, err := readFileTolerant(rcPath)
	if err != nil {
		return rcPath, false, err
	}
	if !strings.Contains(current, pathBlockStart) {
		return rcPath, false, nil
	}
	updated := removeBlock(current, pathBlockStart, pathBlockEnd)
	if updated == current {
		return rcPath, false, nil
	}
	if err := atomicWrite(rcPath, []byte(updated), 0o644); err != nil {
		return rcPath, false, err
	}
	return rcPath, true, nil
}
