package install

import (
	"os"
	"syscall"
)

// usableStdHandle reports whether f's underlying HANDLE can be handed to a child
// process as one of its standard handles.
//
// A Windows process started without standard handles — the Blamely daemon is
// launched with CREATE_NO_WINDOW, and a Scheduled Task or a Startup shortcut
// gives its child none either — still has os.Stdin/os.Stdout/os.Stderr: the os
// package wraps whatever GetStdHandle returned, which in that case is NULL or
// INVALID_HANDLE_VALUE. Passing one of those to exec.Cmd is not inert:
// syscall.StartProcess sets STARTF_USESTDHANDLES unconditionally and duplicates
// each handle it considers non-zero. INVALID_HANDLE_VALUE is the
// current-process pseudo-handle, so the duplicate SUCCEEDS and CreateProcess is
// then handed a PROCESS handle where it expects a file or pipe — which fails
// with ERROR_NOT_SUPPORTED ("İstek desteklenmiyor" on a Turkish install).
//
// That is what broke the daemon's auto-update: the staged binary ran fine for
// the `--version` sanity gate (CombinedOutput uses pipes) and then failed to
// start for the install hand-off moments later, on the same path.
func usableStdHandle(f *os.File) bool {
	if f == nil {
		return false
	}
	h := syscall.Handle(f.Fd())
	return h != 0 && h != syscall.InvalidHandle
}
