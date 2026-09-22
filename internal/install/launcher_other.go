//go:build !windows

package install

// CopyLauncher is Windows-only: the console-less blamelyw.exe launcher exists
// because Task Scheduler shows a console window for console-subsystem
// programs. launchd and systemd have no such problem. No-op here.
func CopyLauncher(string) (string, error) { return "", nil }
