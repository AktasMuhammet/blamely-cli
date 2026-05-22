package tools

// Clipboard reader used by HumanEditWatcher to distinguish typed-by-user
// edits from "pasted from somewhere" edits. We shell out to the standard
// per-OS clipboard command rather than depend on a Go library — clipboards
// are intrinsically OS calls and the CLI tools we use are nearly always
// preinstalled on developer machines (macOS pbpaste; Windows PowerShell;
// xclip / wl-paste on Linux desktops).
//
// All failure modes are treated as "clipboard unavailable" — the watcher
// only loses precision (a paste that would have been labelled `copypaste`
// stays as `human`), never correctness.

import (
	"bytes"
	"context"
	"os/exec"
	"runtime"
	"time"
)

// clipboardReadTimeout bounds how long we wait for an external command
// (pbpaste etc.) to return. Reads run on every workspace file write, so a
// hung clipboard helper would otherwise stall the watcher. 500ms is plenty
// for any healthy clipboard daemon.
const clipboardReadTimeout = 500 * time.Millisecond

// clipboardMaxBytes caps the captured clipboard text. A huge clipboard
// (e.g. binary in the wrong format, or a multi-MB blob the user copied)
// shouldn't blow up the watcher's memory or string-comparison cost.
const clipboardMaxBytes = 4 << 20 // 4 MiB

// ReadClipboard returns the current clipboard text as a UTF-8 string, or ""
// if the clipboard is unavailable, empty, or the OS helper failed. It never
// returns an error — callers treat the result as best-effort: empty means
// "no clipboard signal", any string is checked against the file change.
func ReadClipboard() string {
	cmd := clipboardCommand()
	if cmd == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), clipboardReadTimeout)
	defer cancel()
	cmd = exec.CommandContext(ctx, cmd.Path, cmd.Args[1:]...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	if len(out) > clipboardMaxBytes {
		out = out[:clipboardMaxBytes]
	}
	// Some helpers append a trailing newline (PowerShell's Get-Clipboard
	// does on multi-line content); trim only the very last one so the
	// content-equality check against an editor paste doesn't false-miss.
	out = bytes.TrimRight(out, "\r\n")
	return string(out)
}

// clipboardCommand returns the per-platform helper to invoke. The returned
// Cmd is used only to extract Path + Args; the real exec happens through
// CommandContext in ReadClipboard so we can apply the timeout.
func clipboardCommand() *exec.Cmd {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("pbpaste")
	case "windows":
		// PowerShell is universally available on supported Windows versions.
		// -NoProfile keeps startup fast; -Command Get-Clipboard reads the
		// current text clipboard. Non-text formats return empty.
		return exec.Command("powershell.exe", "-NoProfile", "-Command", "Get-Clipboard")
	default:
		// Linux / *BSD. Wayland first (wl-paste) since Wayland sessions
		// often don't expose an X11 clipboard. Fall back to xclip then
		// xsel. We can't probe these synchronously here without doing the
		// actual exec — return the first whose binary is in PATH.
		if p, err := exec.LookPath("wl-paste"); err == nil {
			return &exec.Cmd{Path: p, Args: []string{p, "--no-newline"}}
		}
		if p, err := exec.LookPath("xclip"); err == nil {
			return &exec.Cmd{Path: p, Args: []string{p, "-selection", "clipboard", "-o"}}
		}
		if p, err := exec.LookPath("xsel"); err == nil {
			return &exec.Cmd{Path: p, Args: []string{p, "--clipboard", "--output"}}
		}
		return nil
	}
}
