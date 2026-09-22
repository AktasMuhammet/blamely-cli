//go:build !windows

package install

// The windowless launcher is a Windows-only workaround (see cmd/blamelyw):
// launchd and systemd start the daemon with no console anywhere near it, so
// there is nothing to install and nothing to point at.

// launcherPath never reports a launcher off Windows, which is what makes every
// autostart path use the binary directly.
func launcherPath(string) (string, bool) { return "", false }

// syncLauncher is a no-op off Windows.
func syncLauncher(_, _ string) error { return nil }
