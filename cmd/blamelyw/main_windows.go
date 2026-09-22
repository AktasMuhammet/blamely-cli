//go:build windows

// Command blamelyw is the windowless launcher for the Blamely daemon. Its only
// job is to start `blamely.exe daemon --background` with no console attached,
// and exit.
//
// # WHY IT EXISTS
//
// The Windows autostart entries (the ONLOGON Scheduled Task, the 15-minute
// keepalive task, the Startup shortcut) used to name blamely.exe directly.
// blamely.exe is a CONSOLE-subsystem binary, and Task Scheduler launches a task
// with an interactive token, so Windows allocates that process a real, VISIBLE
// console: a cmd window pops up on screen. The daemon does hide it itself
// (hideConsole, see cmd/blamely/console_windows.go), but only once the Go
// runtime and the cobra command tree are up — a few hundred milliseconds later.
// The result was a console window flashing open and shut every 15 minutes,
// reported by users as exactly that. Task Scheduler has no "don't show a window"
// setting for an interactive task; the ONLY thing that reliably never gets a
// console is a binary whose PE header says GUI subsystem. Hence this file, built
// with `-ldflags -H=windowsgui` — the same split Windows itself ships as
// python.exe/pythonw.exe and java.exe/javaw.exe.
//
// # WHY IT IS THIS SMALL, AND HARDCODED
//
// A signed "run anything you're told, hidden" helper is a malware primitive, and
// the shape of the install matters here: an unsigned .vbs run through wscript on
// a 2-minute timer is what got an earlier layout flagged as
// Trojan:Win32/Commando.A!ml (see internal/install/agent_windows.go). So this
// takes NO arguments, reads no config, and can start exactly one thing: the
// blamely.exe sitting next to it, with a fixed argument list. It is signed by
// the same publisher as blamely.exe in the release pipeline, and everything it
// does is visible in these forty lines.
//
// It is also strictly optional. Every autostart entry falls back to
// `blamely.exe daemon --background` when this file isn't next to the installed
// binary (see install.launcherPath), which is what an older install, a
// hand-built dev binary, or a partial upgrade looks like — those keep the old
// flashing-console behavior, nothing more.
// The committed resource_windows_*.syso files give this binary an icon and the
// version/company/description fields Explorer, Task Manager and every AV console
// show. An anonymous, metadata-less executable that starts another executable
// from a scheduled task is exactly the profile a heuristic scanner — and a
// security reviewer reading a process list — treats as suspicious, so the
// metadata is part of the same trust story as the Authenticode signature the
// release pipeline adds. Regenerate after editing versioninfo.json:
//
//	go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo@v1.7.0 \
//	  -64 -icon packaging/windows/blamely.ico \
//	  -o cmd/blamelyw/resource_windows_amd64.syso cmd/blamelyw/versioninfo.json
//	go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo@v1.7.0 \
//	  -64 -arm -icon packaging/windows/blamely.ico \
//	  -o cmd/blamelyw/resource_windows_arm64.syso cmd/blamelyw/versioninfo.json
//
// The release build overwrites them with the tag's version (see release.yml), so
// the committed copies only ever version a local build.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/blamely/blamely/internal/config"
	"github.com/blamely/blamely/internal/procattr"
)

// daemonExe is the only program this launcher may start. It is resolved next to
// the launcher's own path, never from PATH or an argument.
const daemonExe = "blamely.exe"

func main() {
	self, err := os.Executable()
	if err != nil {
		fail("cannot locate own path: %v", err)
	}
	target := filepath.Join(filepath.Dir(self), daemonExe)
	if _, err := os.Stat(target); err != nil {
		fail("%s not found next to the launcher: %v", daemonExe, err)
	}

	// procattr.Hide supplies CREATE_NO_WINDOW: the daemon is a console program,
	// so without it Windows would give this console-less parent's child a brand
	// new console of its own — the very window this binary exists to avoid.
	//
	// `--background` is kept for parity with the direct-launch fallback. With no
	// console window to drop it is a no-op (hideConsole bails out), and keeping
	// it means both paths run the identical command line.
	cmd := procattr.Hide(exec.Command(target, "daemon", "--background"))
	if err := cmd.Start(); err != nil {
		fail("start %s: %v", target, err)
	}
	// Detach: the daemon must outlive this launcher, which exits immediately so
	// Task Scheduler records the run as finished.
	_ = cmd.Process.Release()
}

// fail records why the daemon could not be started and exits non-zero.
//
// A GUI-subsystem binary has nowhere to print — no console, no stderr anyone
// will read — and this runs unattended from a Scheduled Task, so the only useful
// place for the reason is the daemon's own log, where support already looks.
// The non-zero exit code additionally surfaces in the task's "Last Run Result".
func fail(format string, args ...any) {
	if path, err := config.LogFile(); err == nil {
		if f, ferr := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); ferr == nil {
			fmt.Fprintf(f, "%s launcher: %s\n",
				time.Now().Format("2006/01/02 15:04:05"), fmt.Sprintf(format, args...))
			_ = f.Close()
		}
	}
	os.Exit(1)
}
