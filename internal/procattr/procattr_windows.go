//go:build windows

// Package procattr keeps subprocesses from flashing a console window on
// Windows.
//
// A process launched with CREATE_NO_WINDOW — which is how the daemon is started
// (see install.startDaemonNow) — has no console of its own. When such a process
// runs a CONSOLE program, Windows allocates a brand new console for the child
// and shows it: a cmd window that pops up and vanishes. The daemon shells out to
// git constantly (repo id, toplevel, branch, rev-parse on every hook event and
// watcher signal), so the user sees a window flash over and over; uninstall adds
// its own from taskkill, schtasks, tasklist and powershell.
//
// Passing CREATE_NO_WINDOW down to the child suppresses that console. It does
// not affect output: stdout/stderr still go to whatever pipes the caller set up,
// which is how every one of these call sites reads its result.
package procattr

import (
	"os/exec"
	"syscall"
)

// createNoWindow is CREATE_NO_WINDOW. Spelled out rather than imported so this
// file needs nothing beyond syscall.
const createNoWindow = 0x08000000

// Hide marks cmd so the child gets no console window, and returns cmd so it can
// wrap an exec.Command call inline. Any CreationFlags the caller already set are
// preserved.
func Hide(cmd *exec.Cmd) *exec.Cmd {
	if cmd == nil {
		return cmd
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
	return cmd
}
