//go:build !windows

package main

// initConsole is a no-op off Windows: Linux and macOS terminals decode UTF-8
// and interpret ANSI escapes natively.
func initConsole() {}

// hideConsole is a no-op off Windows: launchd and systemd start the daemon with
// no controlling terminal, so there is no window to hide.
func hideConsole() {}
