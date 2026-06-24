//go:build !windows

package install

// promptCloseBlockers is a no-op off Windows: Unix unlinks files that are still
// open, so an open editor or running daemon never blocks the uninstall.
func promptCloseBlockers() {}
