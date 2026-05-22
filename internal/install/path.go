package install

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/blamely/blamely/internal/config"
)

// blamely PATH install/uninstall.
//
// We append a small marker-delimited block to the user's shell rc so they can
// run `blamely` from any terminal after `blamely install`, and we can cleanly
// remove the same block on `blamely uninstall`.
//
// Marker format (so we can locate the block deterministically without
// touching anything around it):
//
//   # >>> blamely path >>>
//   export PATH="$HOME/.blamely/bin:$PATH"
//   # <<< blamely path <<<
//
// For fish, the syntax differs:
//   # >>> blamely path >>>
//   fish_add_path "$HOME/.blamely/bin"
//   # <<< blamely path <<<

const (
	pathBlockStart = "# >>> blamely path >>>"
	pathBlockEnd   = "# <<< blamely path <<<"
)

// InstallPathEntry appends the blamely-path block to the user's shell rc file
// (chosen from $SHELL). Returns the rc path, whether anything was added (vs.
// already present), and any error. Idempotent: if the marker block is already
// present, returns added=false and leaves the file untouched.
func InstallPathEntry() (rcPath string, added bool, err error) {
	rcPath, kind, err := detectShellRC()
	if err != nil {
		return "", false, err
	}

	block, err := renderPathBlock(kind)
	if err != nil {
		return rcPath, false, err
	}

	current, err := readFileTolerant(rcPath)
	if err != nil {
		return rcPath, false, err
	}
	if strings.Contains(current, pathBlockStart) {
		return rcPath, false, nil
	}

	// Ensure the rc file ends with a newline before appending so our block
	// starts on its own line.
	var sep string
	if len(current) > 0 && !strings.HasSuffix(current, "\n") {
		sep = "\n"
	}
	updated := current + sep + block
	if err := os.MkdirAll(filepath.Dir(rcPath), 0o755); err != nil {
		return rcPath, false, fmt.Errorf("mkdir %s: %w", filepath.Dir(rcPath), err)
	}
	if err := atomicWrite(rcPath, []byte(updated), 0o644); err != nil {
		return rcPath, false, err
	}
	return rcPath, true, nil
}

// UninstallPathEntry removes the marker-delimited blamely path block from the
// rc file recorded by the install. If rcPath is empty, falls back to
// detecting the current shell's rc. Returns true if a block was removed.
func UninstallPathEntry(rcPath string) (string, bool, error) {
	if rcPath == "" {
		p, _, err := detectShellRC()
		if err != nil {
			return "", false, err
		}
		rcPath = p
	}
	current, err := readFileTolerant(rcPath)
	if err != nil {
		return rcPath, false, err
	}
	if !strings.Contains(current, pathBlockStart) {
		return rcPath, false, nil
	}
	updated := removeBlock(current, pathBlockStart, pathBlockEnd)
	if updated == current {
		return rcPath, false, nil
	}
	if err := atomicWrite(rcPath, []byte(updated), 0o644); err != nil {
		return rcPath, false, err
	}
	return rcPath, true, nil
}

// shellKind tracks which syntax to emit in the rc block.
type shellKind int

const (
	shellPosix shellKind = iota // zsh, bash, sh, dash, ksh
	shellFish
)

// detectShellRC chooses an rc file to write to based on $SHELL. It returns
// the absolute path and the shell kind. Windows is unsupported; PATH there is
// managed via the registry, which we don't touch.
func detectShellRC() (string, shellKind, error) {
	if runtime.GOOS == "windows" {
		return "", shellPosix, errors.New("PATH auto-install not supported on Windows")
	}
	home, err := config.Home()
	if err != nil {
		return "", shellPosix, err
	}
	shellPath := os.Getenv("SHELL")
	base := strings.ToLower(filepath.Base(shellPath))
	switch {
	case strings.Contains(base, "fish"):
		return filepath.Join(home, ".config", "fish", "config.fish"), shellFish, nil
	case strings.Contains(base, "zsh"):
		return filepath.Join(home, ".zshrc"), shellPosix, nil
	case strings.Contains(base, "bash"):
		// On macOS, interactive bash usually reads .bash_profile. Prefer it if
		// it already exists; otherwise fall back to .bashrc.
		if runtime.GOOS == "darwin" {
			bp := filepath.Join(home, ".bash_profile")
			if pathExists(bp) {
				return bp, shellPosix, nil
			}
		}
		return filepath.Join(home, ".bashrc"), shellPosix, nil
	default:
		// Unknown shell: default to ~/.profile (POSIX-sourced by login shells).
		return filepath.Join(home, ".profile"), shellPosix, nil
	}
}

func renderPathBlock(kind shellKind) (string, error) {
	binDir, err := config.BlamelyDir()
	if err != nil {
		return "", err
	}
	binDir = filepath.Join(binDir, "bin")
	// Use $HOME-relative form when possible so the line survives moves of the
	// user's home dir (rare but harmless).
	home, _ := config.Home()
	display := binDir
	if home != "" && strings.HasPrefix(binDir, home) {
		display = "$HOME" + strings.TrimPrefix(binDir, home)
	}
	switch kind {
	case shellFish:
		return fmt.Sprintf("%s\nfish_add_path \"%s\"\n%s\n", pathBlockStart, display, pathBlockEnd), nil
	default:
		return fmt.Sprintf("%s\nexport PATH=\"%s:$PATH\"\n%s\n", pathBlockStart, display, pathBlockEnd), nil
	}
}

func readFileTolerant(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return string(data), nil
}

// removeBlock returns content with the [start..end] marker block stripped
// (inclusive of both marker lines). If the trailing newline immediately after
// the end marker would leave a doubled blank line, one extra newline is
// dropped too. Idempotent if the markers are absent.
func removeBlock(content, start, end string) string {
	i := strings.Index(content, start)
	if i < 0 {
		return content
	}
	// Walk back to the start of the line containing the start marker so we
	// don't leave a half-erased line if the marker shares a line with text
	// (it shouldn't, but be defensive).
	lineStart := i
	for lineStart > 0 && content[lineStart-1] != '\n' {
		lineStart--
	}
	j := strings.Index(content[i:], end)
	if j < 0 {
		// Start marker without matching end — leave the file alone rather than
		// truncating user content past the start marker.
		return content
	}
	endIdx := i + j + len(end)
	// Consume the newline after the end marker.
	if endIdx < len(content) && content[endIdx] == '\n' {
		endIdx++
	}
	// If removing the block leaves a doubled newline (was: ...\n<block>\n... →
	// ...\n\n...), drop one of the surrounding newlines to keep formatting
	// clean.
	out := content[:lineStart] + content[endIdx:]
	out = strings.Replace(out, "\n\n\n", "\n\n", 1)
	return out
}
