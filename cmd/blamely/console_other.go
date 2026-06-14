//go:build !windows

package main

// initConsole is a no-op off Windows: Linux and macOS terminals decode UTF-8
// and interpret ANSI escapes natively.
func initConsole() {}
