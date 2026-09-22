//go:build windows

package install

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRunDetachedShell_DeliversQuotedPaths is the regression test for the
// uninstall cleanup that silently did nothing.
//
// The script was passed as an argv element (exec.Command("cmd", "/c", script)).
// os/exec escapes a double quote inside an argument as `\"` — the convention
// CommandLineToArgvW parses — but cmd.exe has no backslash escape, so it read
// the backslash literally: `del /f /q \"C:\...\"`. Every path and task name in
// the script arrived corrupted, every command failed, and every failure was
// swallowed by the script's own `>nul 2>&1`, leaving ~/.blamely on disk after a
// "successful" uninstall.
//
// The paths here contain spaces, which cannot be expressed without quotes, so a
// regression fails this test instead of passing quietly.
//
// Only the harmless half of the real script is exercised (del + rmdir); the
// taskkill and schtasks lines would tear down the developer's own daemon.
func TestRunDetachedShell_DeliversQuotedPaths(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "bin dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(dir, "sqlite3.exe") // wiped with the directory
	data := filepath.Join(root, "db file.sqlite")
	for _, f := range []string{locked, data} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	script := fmt.Sprintf(`del /f /q "%s" >nul 2>&1 & rmdir /s /q "%s" >nul 2>&1`, data, dir)
	if err := runDetachedShell(script); err != nil {
		t.Fatal(err)
	}

	// The shell is detached, so poll rather than wait on it.
	gone := func(p string) bool { _, err := os.Stat(p); return os.IsNotExist(err) }
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if gone(data) && gone(dir) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !gone(data) {
		t.Errorf("quoted file %s survived: the script did not reach cmd.exe intact", data)
	}
	if !gone(dir) {
		t.Errorf("quoted directory %s survived: the script did not reach cmd.exe intact", dir)
	}
}

// TestBinCleanupScript_KeepsTreeKill guards the one invariant the synchronous
// kills now depend on.
//
// killProcess and killOtherDaemonProcesses deliberately DROPPED /t: during an
// update the process they were killing is the parent of the running installer,
// so a tree kill took the installer down with it and the machine ended up with
// no daemon at all until the 15-minute keepalive fired. That trade is only safe
// because this deferred script still sweeps child trees — after every blamely
// process of ours has exited — and still deletes the respawn tasks first so a
// keepalive can't start a new daemon mid-cleanup.
func TestBinCleanupScript_KeepsTreeKill(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin", "blamely.exe")
	data := filepath.Join(root, "db file.sqlite")
	script := binCleanupScript(bin, []string{data}, "")

	if !strings.Contains(script, `taskkill /f /t /im "blamely.exe"`) {
		t.Errorf("the deferred cleanup must still kill child trees (/t):\n%s", script)
	}
	for _, tn := range []string{scheduledTaskName, keepaliveTaskName, legacyWatchdogTaskName} {
		if !strings.Contains(script, fmt.Sprintf(`schtasks /delete /f /tn "%s"`, tn)) {
			t.Errorf("cleanup does not delete task %q, so it could be resurrected mid-cleanup", tn)
		}
	}
	// A path with a space must arrive quoted, or cmd.exe splits it.
	if !strings.Contains(script, `"`+data+`"`) {
		t.Errorf("data file not quoted in:\n%s", script)
	}
	if !strings.Contains(script, `rmdir /s /q "`+filepath.Dir(bin)+`"`) {
		t.Errorf("bin dir not wiped in:\n%s", script)
	}
}
