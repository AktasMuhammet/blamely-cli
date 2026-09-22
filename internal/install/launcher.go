package install

import "path/filepath"

// launcherName is the windowless daemon launcher that ships next to the Windows
// binary (see cmd/blamelyw): a GUI-subsystem exe whose only job is to start
// `blamely.exe daemon --background` without letting Windows put a console window
// on screen. The autostart entries point at it when it is present, and fall back
// to blamely.exe itself when it is not.
//
// Declared in this platform-neutral file rather than in launcher_windows.go
// because the release-archive extraction (update.go) needs the name on every
// host: a macOS box never installs it, but the same code path reads Windows
// archives in tests.
const launcherName = "blamelyw.exe"

// isLauncherEntry reports whether an archive entry is the windowless launcher.
// Matched on the BASE name for the same reason isBinaryEntry is: the enclosing
// directory is blamely_v1.8.0_windows_amd64/ on a semver release and
// blamely_windows_amd64/ on the rolling channels.
func isLauncherEntry(name string) bool {
	return filepath.Base(filepath.FromSlash(name)) == launcherName
}
