//go:build !windows

package procattr

import "os/exec"

// Hide is a no-op off Windows: only Windows gives a console-less parent's child
// a console window of its own. See procattr_windows.go for what this exists for.
func Hide(cmd *exec.Cmd) *exec.Cmd { return cmd }
