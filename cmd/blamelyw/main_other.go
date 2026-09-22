//go:build !windows

// blamelyw exists only to work around a Windows console-window behavior (see
// main_windows.go). This stub keeps `go build ./...` and `go vet ./...` honest on
// macOS and Linux, where launchd and systemd start the daemon with no console in
// the first place and nothing needs a launcher.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "blamelyw is the Windows-only windowless daemon launcher; run `blamely daemon` instead")
	os.Exit(1)
}
