//go:build windows

package install

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/blamely/blamely/internal/config"
)

// launcherName is the console-less companion binary (cmd/blamelyw, built with
// -H=windowsgui) that the autostart tasks run instead of blamely.exe, so that
// Task Scheduler never creates a visible console window. It ships next to
// blamely.exe in the release archive and installer.
const launcherName = "blamelyw.exe"

// InstalledLauncherPath is where CopyLauncher keeps the stable copy of
// blamelyw.exe: the same bin dir as the installed binary, because the launcher
// resolves its target as "blamely.exe next to me".
func InstalledLauncherPath() (string, error) {
	dir, err := config.BlamelyDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "bin", launcherName), nil
}

// CopyLauncher installs blamelyw.exe from next to the running binary into the
// bin dir. It returns ("", nil) when no launcher ships next to the binary — a
// dev build or a pre-launcher archive — in which case the autostart entries
// fall back to running blamely.exe directly (functional, but a console window
// flashes on each keepalive fire; see daemonLaunchTarget in agent_windows.go).
func CopyLauncher(srcBinPath string) (string, error) {
	src := filepath.Join(filepath.Dir(srcBinPath), launcherName)
	if _, err := os.Stat(src); err != nil {
		return "", nil
	}
	dst, err := InstalledLauncherPath()
	if err != nil {
		return "", err
	}
	if same, _ := sameFile(src, dst); same {
		// Re-running `install` from ~/.blamely/bin — already in place.
		return dst, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", filepath.Dir(dst), err)
	}

	in, err := os.Open(src)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".blamely-launcher-*")
	if err != nil {
		return "", fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return "", fmt.Errorf("copy: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return "", err
	}
	// placeBinary handles the (unlikely) case of a launcher image still mapped
	// by a keepalive fire mid-copy the same way it does for blamely.exe: the
	// locked file is renamed aside and reaped later.
	if err := placeBinary(tmpPath, dst); err != nil {
		return "", err
	}
	return dst, nil
}
