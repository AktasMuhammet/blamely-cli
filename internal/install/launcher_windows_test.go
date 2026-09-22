//go:build windows

package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLauncherPath(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "blamely.exe")
	if err := os.WriteFile(bin, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}

	p, ok := launcherPath(bin)
	if ok {
		t.Error("launcher reported present when the file does not exist")
	}
	if want := filepath.Join(dir, launcherName); p != want {
		t.Errorf("path = %q, want %q (must be a sibling of the binary)", p, want)
	}

	if err := os.WriteFile(p, []byte("launcher"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := launcherPath(bin); !ok {
		t.Error("launcher not reported present after it was written")
	}
}

// A directory named blamelyw.exe must not be mistaken for the launcher: the
// autostart entry would point at something unrunnable and the daemon would never
// start, where the fallback would have worked.
func TestLauncherPath_IgnoresDirectory(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "blamely.exe")
	if err := os.WriteFile(bin, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, launcherName), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := launcherPath(bin); ok {
		t.Error("a directory was accepted as the launcher")
	}
}

func TestSyncLauncher(t *testing.T) {
	src := filepath.Join(t.TempDir(), "blamely.exe")
	dst := filepath.Join(t.TempDir(), "blamely.exe")
	for _, p := range []string{src, dst} {
		if err := os.WriteFile(p, []byte("bin"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Nothing shipped next to src: a no-op, not an error. This is every dev
	// build (`go build ./cmd/blamely`).
	if err := syncLauncher(src, dst); err != nil {
		t.Fatalf("syncLauncher with no launcher present: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dst), launcherName)); !os.IsNotExist(err) {
		t.Fatalf("stat = %v, want not-exist", err)
	}

	if err := os.WriteFile(filepath.Join(filepath.Dir(src), launcherName), []byte("launcher"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syncLauncher(src, dst); err != nil {
		t.Fatalf("syncLauncher: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(dst), launcherName))
	if err != nil {
		t.Fatalf("launcher not installed: %v", err)
	}
	if string(b) != "launcher" {
		t.Errorf("installed launcher = %q, want launcher", b)
	}

	// Re-running install from the installed copy (src == dst) must not touch it.
	if err := syncLauncher(dst, dst); err != nil {
		t.Errorf("syncLauncher(dst, dst): %v", err)
	}
}

// The command line the Scheduled Tasks and the Startup shortcut run: the
// launcher when it is installed (no arguments — it only knows how to start the
// daemon), otherwise the pre-launcher form, which still works and only costs the
// console flash.
func TestDaemonCommand_PrefersLauncher(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "blamely.exe")
	if err := os.WriteFile(bin, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := daemonCommand(bin)
	if want := `"` + bin + `" daemon --background`; got != want {
		t.Errorf("without launcher: %q, want %q", got, want)
	}

	launcher := filepath.Join(dir, launcherName)
	if err := os.WriteFile(launcher, []byte("launcher"), 0o755); err != nil {
		t.Fatal(err)
	}
	got = daemonCommand(bin)
	if want := `"` + launcher + `"`; got != want {
		t.Errorf("with launcher: %q, want %q", got, want)
	}
	if strings.Contains(got, "daemon") {
		t.Errorf("launcher must be invoked with no arguments, got %q", got)
	}
}
