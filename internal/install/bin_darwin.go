//go:build darwin

package install

import (
	"fmt"
	"os/exec"
)

// prepareInstalledBinary makes a freshly-installed binary safe to run under
// macOS Gatekeeper. Two things bite downloaded CLIs on macOS — especially on
// Apple Silicon, where the kernel refuses to exec a binary with a missing or
// invalid code signature (the infamous "Killed: 9"):
//
//  1. com.apple.quarantine — the extended attribute browsers/extractors attach
//     to anything pulled from the internet. Left in place, Gatekeeper blocks or
//     translocates the binary.
//  2. A stale/ad-hoc signature that doesn't validate on this machine.
//
// We strip the quarantine flag and ad-hoc re-sign in place so the stable copy
// in ~/.blamely/bin (the one the daemon agent and git hook actually launch)
// always runs. Both steps are best-effort: a missing attribute or an absent
// codesign tool must not fail an otherwise-successful install.
func prepareInstalledBinary(path string) error {
	// `-d` removes a single attribute; `-r` recurses (harmless on a file).
	// A missing attribute makes xattr exit non-zero — ignore it.
	if _, err := exec.LookPath("xattr"); err == nil {
		_ = exec.Command("xattr", "-dr", "com.apple.quarantine", path).Run()
	}

	// Ad-hoc re-sign ("-" identity) so the Mach-O carries a signature valid for
	// this machine. codesign ships with macOS at /usr/bin/codesign, but guard
	// anyway and stay best-effort.
	if _, err := exec.LookPath("codesign"); err == nil {
		if out, err := exec.Command("codesign", "--force", "--sign", "-", path).CombinedOutput(); err != nil {
			return fmt.Errorf("ad-hoc re-sign %s: %w: %s", path, err, out)
		}
	}
	return nil
}
