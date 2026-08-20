//go:build windows

package install

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/blamely/blamely/internal/procattr"
)

// killProcess terminates a specific PID (and its child tree) on Windows by PID
// alone — no IMAGENAME filter. Targeting the exact PID is both reliable (taskkill
// filter wildcards are finicky and an exact image name misses a renamed/dev
// daemon) and safe to call from the uninstaller: it's the daemon's PID, read
// from the daemon's own PID file, never our own (killRunningDaemon guards
// against pid == os.Getpid()). /T also reaps the daemon's child processes.
func killProcess(pid int) error {
	return procattr.Hide(exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid))).Run()
}

// killOtherDaemonProcesses synchronously kills every blamely.exe EXCEPT this
// uninstaller. `/FI "PID ne <self>"` is what makes `/IM blamely.exe` safe to call
// from a process that is itself blamely.exe — it spares us while reaping the
// daemon (and any leftover daemons from earlier upgrades), by the real, exact
// image name, with no PID-file or filter-wildcard dependency. /T takes child
// trees. Synchronous (Run waits), so the daemon is dead before the detached bin
// cleanup runs — the fix for "uninstall removed the files but blamely kept
// running and kept the dir locked".
func killOtherDaemonProcesses() {
	self := "PID ne " + strconv.Itoa(os.Getpid())
	_ = procattr.Hide(exec.Command("taskkill", "/F", "/T", "/IM", "blamely.exe", "/FI", self)).Run()
}

// detachedProcess is DETACHED_PROCESS: the cleanup shell keeps running after
// this process (and its console) exits. CREATE_NO_WINDOW comes from
// procattr.Hide, which every subprocess goes through.
const detachedProcess = 0x00000008

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
//
// extraFiles (db.sqlite, daemon.log, …) are deleted in the SAME script: the
// still-running daemon holds them open, so they can only be removed AFTER the
// taskkill — os.Remove from this still-live process would fail with a sharing
// violation on Windows.
//
// purgeRoot, when non-empty, is ~/.blamely: --purge wipes the whole tree
// (config.json, exclude, and everything else) instead of just the bin dir and
// the listed data files.
func removeInstalledBinary(p string, extraFiles []string, purgeRoot string) error {
	if _, err := os.Stat(p); os.IsNotExist(err) && len(extraFiles) == 0 && purgeRoot == "" {
		return nil
	}
	binDir := filepath.Dir(p)
	exeName := filepath.Base(p) // blamely.exe — the daemon's image name too
	// del the runtime/data files that live in ~/.blamely (NOT in bin): logs,
	// state, db. Each best-effort; trailing " & " so the rmdir concatenates.
	// Skipped on purge — the single rmdir of the whole tree covers them.
	var dels string
	if purgeRoot == "" {
		for _, f := range extraFiles {
			dels += fmt.Sprintf(`del /f /q "%s" >nul 2>&1 & `, f)
		}
	}
	// `ping -n N` is a portable sleep (no `timeout` dependency, which fails when
	// stdin isn't a console). Sequence: wait ~2s for THIS process to exit and
	// release its image lock; kill the still-running daemon (now the only
	// blamely.exe left, and the holder of db.sqlite/daemon.log); wait ~1s for
	// Windows to release those locks; delete the ~/.blamely data files; then
	// wipe the bin directory.
	//
	// rmdir /s /q removes the WHOLE bin folder, not just blamely.exe. The
	// installer also drops sqlite3.exe (bundled for the IDE plugins), staging
	// subdirs (.install-*, .update-*), a legacy blamely-daemon.vbs on machines
	// upgraded from before 1.8.0, and rotated backups (blamely.exe.old-*) into
	// this same folder — a plain del of the exe
	// followed by a non-recursive rmdir left every one of those (and the folder)
	// behind. The bin dir is exclusively Blamely's, so recursive removal is safe;
	// ~/.blamely itself (config.json, exclude, a kept db) is the parent and is
	// untouched. All output suppressed; failures ignored since this runs
	// unattended after we exit.
	//
	// The kill+delete runs in THREE passes with a wait between each. Killing the
	// daemon and deleting are best-effort and racy on Windows: a taskkill can land
	// while the daemon is mid-shutdown, and even after it dies Windows may not have
	// released the exe/db handles by the time rmdir runs. Crucially, `rmdir /s`
	// deletes everything it CAN and SKIPS a locked file — leaving that file AND its
	// parent dir behind. So if blamely.exe (this uninstaller's own image, still
	// exiting) or sqlite3.exe (an open IDE plugin) is locked when the first rmdir
	// runs, the bin dir survives while the rest of the tree is gone. Re-running the
	// rmdir after a wait, several times, reaps the dir once those handles release.
	wipeDir := binDir
	if purgeRoot != "" {
		wipeDir = purgeRoot // wipe the whole ~/.blamely tree
	}
	// taskkill the daemon by its exact image name (proven; filter wildcards are
	// unreliable). /t reaps its child tree. This is the BACKSTOP — the synchronous
	// killProcess(pid) above already killed the recorded daemon, including a
	// renamed/dev one this /im can't match. Safe here: it runs only after the ping
	// wait, once this uninstaller process has exited.
	// Delete the respawn tasks IN the cleanup script too, not just via
	// UninstallDaemonAgent before this. The keepalive fires on a timer; if its
	// schtasks deletion didn't take (or raced), it would spawn a fresh daemon
	// mid-cleanup that re-locks blamely.exe / db.sqlite and defeats the rmdir.
	// Re-deleting the tasks at the top of each pass guarantees no resurrection
	// between the taskkill and the rmdir. The legacy watchdog name is included so
	// uninstalling a machine that upgraded from the pre-1.8.0 VBScript layout
	// can't be resurrected by a task this build never creates.
	var taskDel string
	for _, tn := range []string{scheduledTaskName, keepaliveTaskName, legacyWatchdogTaskName} {
		taskDel += fmt.Sprintf(`schtasks /delete /f /tn "%s" >nul 2>&1 & `, tn)
	}
	killClean := fmt.Sprintf(`%staskkill /f /t /im "%s" >nul 2>&1 & %srmdir /s /q "%s" >nul 2>&1`, taskDel, exeName, dels, wipeDir)
	// First wait is short (~1s): the daemon is already dead (killed synchronously
	// in killRunningDaemon), so the ONLY lock left on the bin is this uninstaller's
	// own image, which releases the instant it exits — right after this returns.
	// The extra passes with longer waits are just insurance against a transient
	// handle (e.g. an editor's sqlite3 query) still open on the first try.
	script := fmt.Sprintf(
		`ping 127.0.0.1 -n 2 >nul & %s & ping 127.0.0.1 -n 3 >nul & %s & ping 127.0.0.1 -n 3 >nul & %s`,
		killClean, killClean, killClean,
	)
	cmd := procattr.Hide(exec.Command("cmd", "/c", script))
	// Run the cleanup from a directory OUTSIDE the tree being deleted, so the
	// detached process's own working directory can never hold a lock that blocks
	// the rmdir. (Inherited CWD could otherwise sit inside ~/.blamely.)
	cmd.Dir = os.TempDir()
	// procattr.Hide already added CREATE_NO_WINDOW; this shell additionally needs
	// DETACHED_PROCESS so it survives our own exit. OR it in rather than
	// replacing SysProcAttr, which would drop what Hide set.
	cmd.SysProcAttr.CreationFlags |= detachedProcess
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("schedule binary removal: %w", err)
	}
	// Release so we don't wait on the detached cleanup; it outlives us.
	_ = cmd.Process.Release()
	return nil
}
