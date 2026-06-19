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
//
// It also kills the background daemon, which is a SECOND running instance of the
// same blamely.exe and holds its own lock on the image — without this, the del
// below fails (file in use) and the bin directory is then left behind because
// rmdir can't remove a non-empty folder. The daemon can only be killed here,
// after we've exited: `taskkill /IM blamely.exe` matches by image name, so doing
// it while the uninstaller is still alive would kill the uninstaller too. The
// leading ping ensures this process is gone first, leaving the daemon as the
// only remaining blamely.exe.
func removeInstalledBinary(p string) error {
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return nil
	}
	binDir := filepath.Dir(p)
	exeName := filepath.Base(p) // blamely.exe — the daemon's image name too
	// `ping -n N` is a portable sleep (no `timeout` dependency, which fails when
	// stdin isn't a console). Sequence: wait ~2s for THIS process to exit and
	// release its image lock; kill the still-running daemon (now the only
	// blamely.exe left); wait ~1s for Windows to release the daemon's lock; then
	// force-delete the exe and rmdir the now-empty bin folder. All output
	// suppressed; failures are ignored since this runs unattended after we exit.
	script := fmt.Sprintf(
		`ping 127.0.0.1 -n 3 >nul & taskkill /f /im "%s" >nul 2>&1 & ping 127.0.0.1 -n 2 >nul & del /f /q "%s" >nul 2>&1 & rmdir "%s" >nul 2>&1`,
		exeName, p, binDir,
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
