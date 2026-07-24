//go:build darwin

package install

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/blamely/blamely/internal/config"
)

const launchAgentLabel = "com.blamely.daemon"

// targetUID is the uid whose per-user launchd (Aqua/GUI) domain the agent must
// live in. Normally that's the caller. But when `blamely install` is run under
// sudo, os.Getuid() is 0 — bootstrapping into gui/0 loads the agent into the
// wrong domain, so it never runs in the actual user's session and the daemon
// silently never starts. Prefer SUDO_UID (the real invoking user) in that case.
func targetUID() int {
	return resolveTargetUID(os.Getuid(), os.Getenv("SUDO_UID"))
}

// resolveTargetUID is the pure core of targetUID (extracted so it's testable
// without being root): given the caller's uid and the SUDO_UID env value, it
// returns the uid whose GUI domain the agent belongs to. Only a root caller
// with a valid, non-zero SUDO_UID is redirected; every other case keeps uid.
func resolveTargetUID(uid int, sudoUID string) int {
	if uid == 0 && sudoUID != "" {
		if n, err := strconv.Atoi(sudoUID); err == nil && n != 0 {
			return n
		}
	}
	return uid
}

// guiDomainTarget is the launchctl service target for our agent in the user's
// Aqua session, e.g. "gui/501/com.blamely.daemon".
func guiDomainTarget(uid int) string {
	return fmt.Sprintf("gui/%d/%s", uid, launchAgentLabel)
}

// guiDomainExists reports whether the user's Aqua launchd domain (e.g.
// "gui/501") exists — i.e. the user is logged into a GUI session. `launchctl
// print <domain>` exits non-zero when the domain doesn't exist, which is the
// normal state during MDM/SSH installs pushed while the user isn't logged in.
func guiDomainExists(domain string) bool {
	return exec.Command("launchctl", "print", domain).Run() == nil
}

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

	uid := targetUID()
	domain := fmt.Sprintf("gui/%d", uid)
	target := guiDomainTarget(uid)

	// Modern bootstrap flow (macOS 10.11+). We deliberately do NOT use the legacy
	// `launchctl load`/`unload`: on current macOS those exit 0 even when they do
	// nothing — most importantly when the label sits in launchd's disabled
	// override database (left by a prior bootout, or set by MDM). That made
	// install report the agent as loaded while the daemon never actually started
	// and left an empty daemon.log. The sequence below is robust:
	//
	//   1. bootout — tear down any previously-bootstrapped instance so bootstrap
	//      doesn't fail with "service already loaded". Harmless if not loaded.
	//   2. enable  — clear any disabled-in-override-DB state. This is the step the
	//      legacy `load` skipped; without it a once-disabled label never starts.
	//   3. bootstrap — actually load the plist into the user's Aqua (gui) domain.
	//   4. kickstart -k — force (re)start so a fresh binary path takes effect now.
	_ = exec.Command("launchctl", "bootout", target).Run()
	_ = exec.Command("launchctl", "enable", target).Run()
	if out, err := exec.Command("launchctl", "bootstrap", domain, plistPath).CombinedOutput(); err != nil {
		// Distinguish "can't start NOW" from "can't start at all". MDM/SSH bulk
		// installs (e.g. a Jamf policy) run while the target user has no Aqua
		// session, so the gui/<uid> domain doesn't exist and bootstrap MUST
		// fail. That's fine: launchd bootstraps ~/Library/LaunchAgents at the
		// next GUI login. Report it as a deferred start so install prints the
		// truth instead of an 8s health-wait timeout + misleading diagnostics.
		if !guiDomainExists(domain) {
			return plistPath, ErrNoGUISession
		}
		// Fall back to legacy load for any environment where bootstrap is
		// unavailable/unhappy, so we still attempt to start the daemon rather
		// than failing the whole install.
		if lout, lerr := exec.Command("launchctl", "load", plistPath).CombinedOutput(); lerr != nil {
			return plistPath, fmt.Errorf("launchctl bootstrap: %v: %s (legacy load also failed: %v: %s)",
				err, string(out), lerr, string(lout))
		}
	}
	// -k = kill if running, then start. Ignored if the daemon wasn't running yet
	// (bootstrap + RunAtLoad already started it).
	_ = exec.Command("launchctl", "kickstart", "-k", target).Run()
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
	// Tear down via bootout (the modern counterpart to load/unload). bootout
	// only unloads — it does NOT add the label to the disabled override DB (that
	// would be `launchctl disable`), so a later reinstall's bootstrap still
	// starts cleanly. Fall back to legacy unload if bootout isn't available.
	target := guiDomainTarget(targetUID())
	if err := exec.Command("launchctl", "bootout", target).Run(); err != nil {
		_ = exec.Command("launchctl", "unload", plistPath).Run()
	}
	return os.Remove(plistPath)
}
