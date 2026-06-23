//go:build !windows

package install

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"syscall"
)

// DropToInvokingUserIfRoot re-executes `blamely <args>` as the user who invoked
// sudo, so an accidental `sudo blamely install` still installs the PER-USER
// daemon (launchd LaunchAgent / `systemd --user`) under that user's home instead
// of root's — where the editor plugins (running as the user) can't reach it.
//
//   - Under sudo (SUDO_USER set): re-exec as that user via `sudo -u <user> -H`.
//     On success this replaces the current process and never returns.
//   - Logged in directly as root (no SUDO_USER): there's no user to fall back to,
//     so warn and continue — keeps legitimate root/container installs working.
//   - Not root: no-op.
func DropToInvokingUserIfRoot() error {
	if syscall.Geteuid() != 0 {
		return nil
	}

	target := os.Getenv("SUDO_USER")
	if target == "" || target == "root" {
		fmt.Fprintln(os.Stderr,
			"  ! Installing as root — Blamely's daemon is per-user, so this sets it up for root. "+
				"Run as your normal user for per-user attribution.")
		return nil
	}

	if _, err := user.Lookup(target); err != nil {
		return fmt.Errorf("resolve sudo user %q: %w", target, err)
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate blamely binary: %w", err)
	}
	sudoPath, err := exec.LookPath("sudo")
	if err != nil {
		return fmt.Errorf("sudo not found to drop privileges to %q: %w", target, err)
	}

	fmt.Fprintf(os.Stderr, "  ! Running under sudo — re-running as '%s' so Blamely installs per-user.\n", target)
	// sudo -u <user> -H <blamely> <original args…>; -H sets HOME to the user's.
	argv := append([]string{"sudo", "-u", target, "-H", self}, os.Args[1:]...)
	return syscall.Exec(sudoPath, argv, os.Environ())
}
