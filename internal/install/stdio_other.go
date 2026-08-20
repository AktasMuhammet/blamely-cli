//go:build !windows

package install

import "os"

// usableStdHandle reports whether f can be handed to a child process as one of
// its standard handles. Only Windows can hand back an unusable std handle from
// a console-less process (see stdio_windows.go); on Unix an open file
// descriptor is always inheritable, and a closed one is a plain nil file.
func usableStdHandle(f *os.File) bool {
	return f != nil
}
