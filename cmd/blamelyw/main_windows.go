//go:build windows

// blamelyw is the console-less launcher the Windows autostart entries run.
//
// blamely.exe is a CONSOLE-subsystem program. When Task Scheduler fires the
// "Blamely Daemon" / "Blamely Daemon Keepalive" tasks in the interactive
// session, Windows creates and SHOWS a console window for it before a single
// line of our code runs — hideConsole() inside `daemon --background` can only
// hide the window after the fact, so the user sees a console flash on every
// keepalive fire (every 15 minutes, by default).
//
// blamelyw is built with `-ldflags -H=windowsgui` (see the release workflow):
// a GUI-subsystem image, for which Windows never creates a console in the
// first place. Its only job is to start the sibling blamely.exe with
// CREATE_NO_WINDOW — same directory, same arguments — and exit. The chain the
// scheduled task produces is "Task Scheduler → blamelyw.exe → blamely.exe",
// both Authenticode-signed, no cmd/powershell/wscript anywhere: this is
// deliberate, see the EDR history in internal/install/agent_windows.go
// (startupShortcutName).
//
// The name follows the pythonw/javaw convention: same product, windowless.
package main

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/blamely/blamely/internal/procattr"
)

func main() {
	self, err := os.Executable()
	if err != nil {
		os.Exit(1)
	}
	bin := filepath.Join(filepath.Dir(self), "blamely.exe")
	if _, err := os.Stat(bin); err != nil {
		// Nothing to launch and (as a GUI process) nowhere to complain.
		os.Exit(1)
	}

	// Pass the arguments through verbatim so the scheduled task's command line
	// stays readable in Task Scheduler ("blamelyw.exe daemon --background").
	cmd := procattr.Hide(exec.Command(bin, os.Args[1:]...))
	if err := cmd.Start(); err != nil {
		os.Exit(1)
	}
	// Detach: the daemon outlives this launcher. Exiting immediately also
	// keeps Task Scheduler's "running" state honest — the task ran, briefly.
	_ = cmd.Process.Release()
}
