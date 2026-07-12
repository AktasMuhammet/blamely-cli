//go:build linux

package install

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/blamely/blamely/internal/config"
)

const systemdUnitName = "blamely.service"

func InstallDaemonAgent(binaryPath string) (string, error) {
	home, err := config.Home()
	if err != nil {
		return "", err
	}
	if _, err := config.EnsureBlamelyDir(); err != nil {
		return "", err
	}
	logPath, err := config.LogFile()
	if err != nil {
		return "", err
	}
	unitDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", unitDir, err)
	}
	unitPath := filepath.Join(unitDir, systemdUnitName)
	unit := fmt.Sprintf(`[Unit]
Description=Blamely AI/Human attribution daemon
After=default.target

[Service]
ExecStart=%s daemon
Restart=always
RestartSec=2
StandardOutput=append:%s
StandardError=append:%s

[Install]
WantedBy=default.target
`, binaryPath, logPath, logPath)
	if err := atomicWrite(unitPath, []byte(unit), 0o644); err != nil {
		return unitPath, err
	}
	// Reload + enable + start. Tolerate systemctl missing (some containers).
	if _, err := exec.LookPath("systemctl"); err != nil {
		return unitPath, fmt.Errorf("systemctl not found: %w", err)
	}
	for _, args := range [][]string{
		{"--user", "daemon-reload"},
		{"--user", "enable", "--now", systemdUnitName},
	} {
		if out, err := exec.Command("systemctl", args...).CombinedOutput(); err != nil {
			return unitPath, fmt.Errorf("systemctl %v: %v: %s", args, err, string(out))
		}
	}
	// Force-restart if the daemon was already running with a stale binary
	// (e.g. on a reinstall after upgrade). `restart` is a no-op when the
	// service has just been started by --now above.
	_ = exec.Command("systemctl", "--user", "restart", systemdUnitName).Run()
	return unitPath, nil
}

// EnsureDaemonAgent is the daemon's startup self-heal. On Linux systemd's
// Restart=always already revives a crashed daemon and the user unit survives
// reboots, so there is nothing to re-assert — restarting the unit from inside
// the daemon would kill the caller. No-op by design; the Windows
// implementation (Scheduled Tasks have no KeepAlive) does the real work.
func EnsureDaemonAgent(string) error { return nil }

func UninstallDaemonAgent() error {
	home, err := config.Home()
	if err != nil {
		return err
	}
	unitPath := filepath.Join(home, ".config", "systemd", "user", systemdUnitName)
	if _, err := os.Stat(unitPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	_ = exec.Command("systemctl", "--user", "disable", "--now", systemdUnitName).Run()
	if err := os.Remove(unitPath); err != nil {
		return err
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	return nil
}
