//go:build darwin

package install

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/blamely/blamely/internal/config"
)

const launchAgentLabel = "com.blamely.daemon"

// InstallDaemonAgent writes a launchd plist under ~/Library/LaunchAgents and
// loads it. Returns the plist path on success.
func InstallDaemonAgent(binaryPath string) (string, error) {
	home, err := config.Home()
	if err != nil {
		return "", err
	}
	plistPath := filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist")
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", filepath.Dir(plistPath), err)
	}
	logPath, err := config.LogFile()
	if err != nil {
		return "", err
	}
	if _, err := config.EnsureBlamelyDir(); err != nil {
		return "", err
	}

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>daemon</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>%s</string>
    <key>StandardErrorPath</key>
    <string>%s</string>
</dict>
</plist>
`, launchAgentLabel, binaryPath, logPath, logPath)

	if err := atomicWrite(plistPath, []byte(plist), 0o644); err != nil {
		return plistPath, err
	}

	// Try to unload an old version first (harmless if not loaded). This
	// ensures the new binary path / new ProgramArguments take effect on
	// reinstall, even if launchd already had the previous version cached.
	_ = exec.Command("launchctl", "unload", plistPath).Run()
	cmd := exec.Command("launchctl", "load", plistPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return plistPath, fmt.Errorf("launchctl load: %v: %s", err, string(out))
	}
	// `launchctl kickstart -k` force-restarts an already-running daemon so a
	// fresh binary picks up changes immediately. -k = kill if running, then
	// start. Ignored if the daemon wasn't running yet (load above started it).
	_ = exec.Command("launchctl", "kickstart", "-k", fmt.Sprintf("gui/%d/%s", os.Getuid(), launchAgentLabel)).Run()
	return plistPath, nil
}

// EnsureDaemonAgent is the daemon's startup self-heal. On macOS launchd's
// KeepAlive already restarts a crashed daemon and the LaunchAgent plist
// survives reboots, so there is nothing to re-assert — reloading the plist
// from inside the daemon would kill the caller. No-op by design; the Windows
// implementation (Scheduled Tasks have no KeepAlive) does the real work.
func EnsureDaemonAgent(string) error { return nil }

func UninstallDaemonAgent() error {
	home, err := config.Home()
	if err != nil {
		return err
	}
	plistPath := filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist")
	if _, err := os.Stat(plistPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	_ = exec.Command("launchctl", "unload", plistPath).Run()
	return os.Remove(plistPath)
}
