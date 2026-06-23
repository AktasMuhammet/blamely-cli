//go:build windows

package install

// DropToInvokingUserIfRoot is a no-op on Windows. UAC elevation keeps the same
// user (same %USERPROFILE%), so an elevated `blamely install` already targets
// the current user's per-user agent — there's no separate root account to drop
// from. The installer scripts warn about running elevated.
func DropToInvokingUserIfRoot() error { return nil }
