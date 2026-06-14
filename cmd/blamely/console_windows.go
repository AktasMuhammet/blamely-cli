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
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procSetConsoleOutputCP = kernel32.NewProc("SetConsoleOutputCP")
	procGetConsoleMode     = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode     = kernel32.NewProc("SetConsoleMode")
)

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
