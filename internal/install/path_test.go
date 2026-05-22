package install

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func setupFakeShellHome(t *testing.T, shell string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("PATH auto-install is not supported on Windows")
	}
	home := fakeHomeDir(t)
	t.Setenv("SHELL", shell)
	return home
}

func TestInstallPathEntry_CreatesRcWhenAbsent(t *testing.T) {
	home := setupFakeShellHome(t, "/bin/zsh")
	rcPath, added, err := InstallPathEntry()
	if err != nil {
		t.Fatalf("InstallPathEntry: %v", err)
	}
	if !added {
		t.Error("expected added=true")
	}
	if rcPath != filepath.Join(home, ".zshrc") {
		t.Errorf("rc path: want %s, got %s", filepath.Join(home, ".zshrc"), rcPath)
	}
	data, err := os.ReadFile(rcPath)
	if err != nil {
		t.Fatalf("read rc: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, pathBlockStart) || !strings.Contains(body, pathBlockEnd) {
		t.Errorf("marker block missing:\n%s", body)
	}
	if !strings.Contains(body, "export PATH=\"$HOME/.blamely/bin:$PATH\"") {
		t.Errorf("expected export line, got:\n%s", body)
	}
}

func TestInstallPathEntry_Idempotent(t *testing.T) {
	setupFakeShellHome(t, "/bin/zsh")
	if _, _, err := InstallPathEntry(); err != nil {
		t.Fatal(err)
	}
	_, added, err := InstallPathEntry()
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if added {
		t.Error("expected added=false on second install")
	}
}

func TestInstallPathEntry_AppendsToExistingRc(t *testing.T) {
	home := setupFakeShellHome(t, "/bin/zsh")
	existing := "# user content\nalias ll='ls -la'\n"
	rcPath := filepath.Join(home, ".zshrc")
	if err := os.WriteFile(rcPath, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := InstallPathEntry(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(rcPath)
	body := string(data)
	if !strings.Contains(body, "alias ll='ls -la'") {
		t.Error("existing user content was lost")
	}
	if !strings.Contains(body, pathBlockStart) {
		t.Error("our block was not appended")
	}
	// Our block must come AFTER the user's content (we only append).
	if strings.Index(body, pathBlockStart) < strings.Index(body, "alias") {
		t.Error("our block was inserted before user content (expected append)")
	}
}

func TestInstallPathEntry_FishShellUsesFishSyntax(t *testing.T) {
	home := setupFakeShellHome(t, "/usr/local/bin/fish")
	rcPath, _, err := InstallPathEntry()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".config", "fish", "config.fish")
	if rcPath != want {
		t.Errorf("rc path: want %s, got %s", want, rcPath)
	}
	data, _ := os.ReadFile(rcPath)
	if !strings.Contains(string(data), "fish_add_path") {
		t.Errorf("fish syntax missing:\n%s", data)
	}
	if strings.Contains(string(data), "export PATH") {
		t.Error("posix export should not appear in fish config")
	}
}

func TestUninstallPathEntry_RemovesBlock_KeepsUserContent(t *testing.T) {
	home := setupFakeShellHome(t, "/bin/zsh")
	rcPath := filepath.Join(home, ".zshrc")
	if err := os.WriteFile(rcPath, []byte("alias x=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := InstallPathEntry(); err != nil {
		t.Fatal(err)
	}
	gotPath, removed, err := UninstallPathEntry(rcPath)
	if err != nil {
		t.Fatalf("UninstallPathEntry: %v", err)
	}
	if !removed {
		t.Error("expected removed=true")
	}
	if gotPath != rcPath {
		t.Errorf("rc path: want %s, got %s", rcPath, gotPath)
	}
	data, _ := os.ReadFile(rcPath)
	body := string(data)
	if strings.Contains(body, pathBlockStart) || strings.Contains(body, pathBlockEnd) {
		t.Errorf("block markers still present:\n%s", body)
	}
	if !strings.Contains(body, "alias x=1") {
		t.Errorf("user content lost:\n%s", body)
	}
}

func TestUninstallPathEntry_NoOp_WhenAbsent(t *testing.T) {
	home := setupFakeShellHome(t, "/bin/zsh")
	rcPath := filepath.Join(home, ".zshrc")
	if err := os.WriteFile(rcPath, []byte("alias x=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, removed, err := UninstallPathEntry(rcPath)
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Error("expected removed=false (no block to remove)")
	}
}

func TestUninstallPathEntry_NoOp_WhenFileAbsent(t *testing.T) {
	setupFakeShellHome(t, "/bin/zsh")
	_, removed, err := UninstallPathEntry("")
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Error("expected removed=false when rc file doesn't exist")
	}
}

func TestRemoveBlock_PreservesSurroundingContent(t *testing.T) {
	src := "before\n" + pathBlockStart + "\nexport PATH=\"x\"\n" + pathBlockEnd + "\nafter\n"
	got := removeBlock(src, pathBlockStart, pathBlockEnd)
	if got != "before\nafter\n" {
		t.Errorf("unexpected result: %q", got)
	}
}

func TestRemoveBlock_NoMarker(t *testing.T) {
	src := "just user content\n"
	if got := removeBlock(src, pathBlockStart, pathBlockEnd); got != src {
		t.Errorf("should be unchanged, got %q", got)
	}
}

func TestRemoveBlock_StartWithoutEnd_LeavesContent(t *testing.T) {
	// Defensive: if the user manually broke the file, never truncate past
	// the start marker.
	src := "before\n" + pathBlockStart + "\nbroken without end marker\nmore content\n"
	got := removeBlock(src, pathBlockStart, pathBlockEnd)
	if got != src {
		t.Errorf("unmatched start should leave content untouched, got %q", got)
	}
}
