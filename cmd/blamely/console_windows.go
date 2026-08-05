//go:build windows

package main

import (
	"os"
	"syscall"
	"unsafe"
)

// Blamely's logs use UTF-8 glyphs (✓ ✗ •) and ANSI color escapes. The Windows
// console doesn't render either by default: it decodes output through the
// active legacy code page (cp850, cp437, cp857 on Turkish systems, …) — turning
// "✓" into mojibake like "Γ£ô" — and it ignores ANSI sequences unless virtual
// terminal processing is enabled on the handle. initConsole fixes both, once,
// at startup. Everything here is best-effort: a failure just leaves the console
// as-is (degraded glyphs), never blocks the command.

const (
	cpUTF8                          = 65001
	enableVirtualTerminalProcessing = 0x0004
)

var (
	kernel32                  = syscall.NewLazyDLL("kernel32.dll")
	procSetConsoleOutputCP    = kernel32.NewProc("SetConsoleOutputCP")
	procGetConsoleMode        = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode        = kernel32.NewProc("SetConsoleMode")
	procGetConsoleWindow      = kernel32.NewProc("GetConsoleWindow")
	procFreeConsole           = kernel32.NewProc("FreeConsole")
	procGetConsoleProcessList = kernel32.NewProc("GetConsoleProcessList")

	user32         = syscall.NewLazyDLL("user32.dll")
	procShowWindow = user32.NewProc("ShowWindow")
)

const swHide = 0

// hideConsole detaches the daemon from the console window the Scheduled Task
// handed it, and is called ONLY for `blamely daemon --background`.
//
// Why this exists: the autostart task now launches the SIGNED blamely.exe
// directly. It used to go through `wscript.exe //B //Nologo <a .vbs>` purely to
// get a hidden window — but that chain (an unsigned script, run by a LOLBin,
// dropped in the Startup folder, re-run every 2 minutes) is the shape of a
// malware persistence template, and Defender's ML classifier flagged it as
// Trojan:Win32/Commando.A!ml. Launching the signed binary directly keeps the
// Authenticode chain intact; hiding its console is then our own job.
//
// SAFETY: when a user runs the daemon by hand from a terminal, GetConsoleWindow
// returns THEIR terminal — hiding it would make their window vanish. So this
// bails out unless we are the only process attached to the console, which is
// true for a task-spawned process with its own fresh console and false for one
// sharing an interactive shell. Everything here is best-effort; the daemon logs
// to ~/.blamely/daemon.log, never to stdout, so a console is nothing it needs.
func hideConsole() {
	if !ownsConsoleAlone() {
		return
	}
	if hwnd, _, _ := procGetConsoleWindow.Call(); hwnd != 0 {
		_, _, _ = procShowWindow.Call(hwnd, uintptr(swHide))
	}
	_, _, _ = procFreeConsole.Call()
}

// ownsConsoleAlone reports whether this process is the ONLY one attached to its
// console. GetConsoleProcessList returns the total attached count even when the
// supplied buffer is too small, so a 2-slot buffer is enough to tell "just us"
// (1) from "sharing a shell" (2+). A zero return means no console at all.
func ownsConsoleAlone() bool {
	var pids [2]uint32
	n, _, _ := procGetConsoleProcessList.Call(
		uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	return n == 1
}

func initConsole() {
	// Decode our UTF-8 output bytes as UTF-8 instead of the legacy OEM code
	// page, so multi-byte glyphs render correctly.
	_, _, _ = procSetConsoleOutputCP.Call(uintptr(cpUTF8))

	// Enable ANSI/VT processing on stdout and stderr so color escapes are
	// interpreted rather than printed literally (Windows 10 1511+).
	enableVT(syscall.Handle(os.Stdout.Fd()))
	enableVT(syscall.Handle(os.Stderr.Fd()))
}

func enableVT(h syscall.Handle) {
	var mode uint32
	ret, _, _ := procGetConsoleMode.Call(uintptr(h), uintptr(unsafe.Pointer(&mode)))
	if ret == 0 {
		// Not a console (e.g. redirected to a file/pipe) — nothing to enable.
		return
	}
	_, _, _ = procSetConsoleMode.Call(uintptr(h), uintptr(mode|enableVirtualTerminalProcessing))
}
