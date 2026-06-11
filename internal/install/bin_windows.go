//go:build windows

package install

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// detachedProcess is CREATE_NO_WINDOW | DETACHED_PROCESS so the cleanup shell
// keeps running after this process (and its console) exits.
const detachedProcess = 0x08000000 | 0x00000008

// removeInstalledBinary cannot delete the currently-running blamely.exe
// directly — Windows locks an executing image, so os.Remove fails with
// "Access is denied" / "being used by another process". Instead it spawns a
// detached cmd.exe that waits for this process to release the lock, deletes the
// exe, and removes the now-empty bin directory.
func removeInstalledBinary(p string) error {
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return nil
	}
	binDir := filepath.Dir(p)
	// `ping -n 3` is a portable ~2s sleep (no `timeout` dependency, which fails
	// when stdin isn't a console). Then force-delete the exe and rmdir the bin
	// folder if it's now empty. All output suppressed; failures are ignored
	// since this runs unattended after we've exited.
	script := fmt.Sprintf(
		`ping 127.0.0.1 -n 3 >nul & del /f /q "%s" >nul 2>&1 & rmdir "%s" >nul 2>&1`,
		p, binDir,
	)
	cmd := exec.Command("cmd", "/c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: detachedProcess,
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("schedule binary removal: %w", err)
	}
	// Release so we don't wait on the detached cleanup; it outlives us.
	_ = cmd.Process.Release()
	return nil
}
