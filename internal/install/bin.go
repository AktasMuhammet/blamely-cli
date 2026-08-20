package install

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/blamely/blamely/internal/config"
)

// recordHookCommand builds the `<binary> record <tool>` command embedded in each
// AI tool's hook config (Claude/Cursor/Codex/Copilot/Gemini). The binary path is
// normalized to forward slashes so the command is byte-identical on every run and
// platform.
//
// Why this matters on Windows: filepath.Join yields a backslash path
// (C:\Users\…\blamely.exe), but the hook files store forward slashes
// (C:/Users/…/blamely.exe). The install step strips the existing blamely entry by
// marker and re-adds a freshly built one, then compares the result — so a
// separator mismatch made it re-add the hook on every run and report it as "not
// detected" instead of recognizing the existing entry. A single canonical form
// (forward slashes, which CreateProcess accepts and which need no JSON/TOML
// escaping) makes the comparison stable. Matching by marker on uninstall is
// unaffected (it's path-agnostic).
//
// A path containing whitespace is quoted: the hook runner executes this command
// through a shell, so an UNQUOTED path with a space (very common on Windows —
// C:\Users\First Last\.blamely\bin\blamely.exe) is split on the space and the
// hook silently never runs. Quoting only when needed keeps the byte-identical /
// idempotent comparison for the common (space-free) path, and the " record <tool>"
// marker suffix is preserved either way.
func recordHookCommand(binaryPath, tool string) string {
	p := filepath.ToSlash(binaryPath)
	if strings.ContainsAny(p, " \t") {
		p = `"` + p + `"`
	}
	return p + " record " + tool
}

// recordHookCommandForEvent is recordHookCommand plus the flag the event needs.
//
// A PreToolUse hook MUST pass --pre. Without it the pre-hook runs the full
// recording path against the file's PRE-edit content, so the AI is credited with
// the lines that were there BEFORE it wrote anything — the user's own work
// included. --pre instead snapshots that content as the diff baseline, which is
// the whole point of hooking the event: the matching PostToolUse record then
// attributes only what the agent actually changed.
//
// The " record <tool>" marker survives the suffix, so uninstall and the
// dedupe-on-reinstall scan (which match on that substring) keep working.
func recordHookCommandForEvent(binaryPath, tool, event string) string {
	cmd := recordHookCommand(binaryPath, tool)
	if isPreEditHookEvent(event) {
		return cmd + " --pre"
	}
	return cmd
}

// preEditHookEvents are the BEFORE-the-tool-runs event names, across every agent's
// own spelling: Claude/Copilot "PreToolUse", Codex "pre_tool_use", Gemini
// "BeforeTool". Compared case-insensitively.
var preEditHookEvents = []string{"pretooluse", "pre_tool_use", "beforetool"}

// isPreEditHookEvent reports whether event fires BEFORE the tool writes.
func isPreEditHookEvent(event string) bool {
	e := strings.ToLower(strings.TrimSpace(event))
	for _, p := range preEditHookEvents {
		if e == p {
			return true
		}
	}
	return false
}

// InstalledBinaryPath returns the path where `blamely install` keeps a stable
// copy of the binary, so the post-commit hook and the daemon agent keep
// working even if the user moves or deletes the dev binary.
//
// Layout: ~/.blamely/bin/blamely  (or blamely.exe on Windows)
func InstalledBinaryPath() (string, error) {
	dir, err := config.BlamelyDir()
	if err != nil {
		return "", err
	}
	name := "blamely"
	if runtime.GOOS == "windows" {
		name = "blamely.exe"
	}
	return filepath.Join(dir, "bin", name), nil
}

// CopyBinary copies the currently running binary to InstalledBinaryPath().
// If src and dst happen to be the same file (the user runs `blamely install`
// from ~/.blamely/bin/blamely), it's a no-op.
func CopyBinary(src string) (string, error) {
	dst, err := InstalledBinaryPath()
	if err != nil {
		return "", err
	}
	// Reap binaries renamed aside by earlier in-place upgrades whose daemon has
	// since exited — keeps ~/.blamely/bin from accumulating blamely.exe.old-*
	// copies on Windows. Best-effort; a copy still locked by a live daemon is
	// skipped and reaped on a later run.
	cleanStaleBinaryBackups(dst)

	if same, _ := sameFile(src, dst); same {
		// Already the stable copy (e.g. re-running `install` from
		// ~/.blamely/bin). Still self-heal: a binary downloaded straight into
		// place may carry a quarantine flag / stale signature.
		_ = prepareInstalledBinary(dst)
		return dst, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", filepath.Dir(dst), err)
	}

	in, err := os.Open(src)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".blamely-bin-*")
	if err != nil {
		return "", fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return "", fmt.Errorf("copy: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return "", err
	}
	if err := placeBinary(tmpPath, dst); err != nil {
		return "", err
	}
	// Make the stable copy Gatekeeper-safe on macOS (strip quarantine +
	// ad-hoc re-sign) so the daemon agent and git hook can launch it without a
	// "Killed: 9". No-op on Linux/Windows. Best-effort — never block install.
	_ = prepareInstalledBinary(dst)
	return dst, nil
}

// placeBinary moves tmpPath onto dst, replacing whatever is there. On Windows
// the dst binary may be locked by the still-running (old) daemon, so a direct
// replace fails with a sharing violation — but Windows DOES allow renaming a
// running image aside. So on failure we move the locked dst out of the way
// (dst.old-<ts>) and retry; the renamed-aside copy is reaped by
// cleanStaleBinaryBackups on a later install once its daemon has exited. On Unix
// the first rename replaces atomically and the fallback never runs.
func placeBinary(tmpPath, dst string) error {
	if err := os.Rename(tmpPath, dst); err == nil {
		return nil
	}
	aside := fmt.Sprintf("%s.old-%d", dst, time.Now().UnixNano())
	if mvErr := os.Rename(dst, aside); mvErr != nil {
		return fmt.Errorf("rename to %s: %w", dst, mvErr)
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		_ = os.Rename(aside, dst) // roll back so we never leave dst missing
		return fmt.Errorf("rename to %s: %w", dst, err)
	}
	return nil
}

// cleanStaleBinaryBackups removes <dst>.old-* copies left by earlier in-place
// upgrades (see placeBinary). Best-effort: a copy still locked by a not-yet-
// exited daemon is skipped and reaped on a later run.
func cleanStaleBinaryBackups(dst string) {
	matches, _ := filepath.Glob(dst + ".old-*")
	for _, m := range matches {
		_ = os.Remove(m)
	}
}

func sameFile(a, b string) (bool, error) {
	sa, ea := os.Stat(a)
	sb, eb := os.Stat(b)
	if ea != nil || eb != nil {
		// If either doesn't exist, they are not the same.
		if errors.Is(ea, os.ErrNotExist) || errors.Is(eb, os.ErrNotExist) {
			return false, nil
		}
		return false, errors.Join(ea, eb)
	}
	return os.SameFile(sa, sb), nil
}
