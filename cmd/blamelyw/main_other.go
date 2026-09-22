//go:build !windows

// blamelyw only means something on Windows (see main_windows.go). This stub
// exists so `go build ./...` and `go vet ./...` succeed on other platforms.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "blamelyw is the Windows console-less launcher; it does nothing on this platform")
	os.Exit(1)
}
