package install

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/blamely/blamely/internal/config"
)

// InstalledBinaryPath returns the path where `blamely install` keeps a stable
// copy of the binary, so the post-commit hook and the daemon agent keep
// working even if the user moves or deletes the dev binary.
//
// Layout: ~/.blamely/bin/blamely  (or blamely.exe on Windows)
func InstalledBinaryPath() (string, error) {
	dir, err := config.BlamelyDir()
	if err != nil {
		return "", err
	}
	name := "blamely"
	if runtime.GOOS == "windows" {
		name = "blamely.exe"
	}
	return filepath.Join(dir, "bin", name), nil
}

// CopyBinary copies the currently running binary to InstalledBinaryPath().
// If src and dst happen to be the same file (the user runs `blamely install`
// from ~/.blamely/bin/blamely), it's a no-op.
func CopyBinary(src string) (string, error) {
	dst, err := InstalledBinaryPath()
	if err != nil {
		return "", err
	}
	if same, _ := sameFile(src, dst); same {
		// Already the stable copy (e.g. re-running `install` from
		// ~/.blamely/bin). Still self-heal: a binary downloaded straight into
		// place may carry a quarantine flag / stale signature.
		_ = prepareInstalledBinary(dst)
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

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".blamely-bin-*")
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
	if err := os.Rename(tmpPath, dst); err != nil {
		return "", fmt.Errorf("rename to %s: %w", dst, err)
	}
	// Make the stable copy Gatekeeper-safe on macOS (strip quarantine +
	// ad-hoc re-sign) so the daemon agent and git hook can launch it without a
	// "Killed: 9". No-op on Linux/Windows. Best-effort — never block install.
	_ = prepareInstalledBinary(dst)
	return dst, nil
}

func sameFile(a, b string) (bool, error) {
	sa, ea := os.Stat(a)
	sb, eb := os.Stat(b)
	if ea != nil || eb != nil {
		// If either doesn't exist, they are not the same.
		if errors.Is(ea, os.ErrNotExist) || errors.Is(eb, os.ErrNotExist) {
			return false, nil
		}
		return false, errors.Join(ea, eb)
	}
	return os.SameFile(sa, sb), nil
}
