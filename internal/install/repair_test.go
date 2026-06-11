package install

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeFakeInstalledBinary creates a stub at InstalledBinaryPath() so
// addMissingToolHooks treats blamely as already installed (its early-return
// guard for "run `blamely install` first").
func writeFakeInstalledBinary(t *testing.T, home string) string {
	t.Helper()
	binDir := filepath.Join(home, ".blamely", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "blamely"
	if runtime.GOOS == "windows" {
		name = "blamely.exe"
	}
	binPath := filepath.Join(binDir, name)
	if err := os.WriteFile(binPath, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	return binPath
}

func hooksAddedContains(result *RepairResult, substr string) bool {
	for _, h := range result.HooksAdded {
		if strings.Contains(h, substr) {
			return true
		}
	}
	return false
}

// TestRepair_ConfiguresMissingToolHooks reproduces "I installed Cursor/Codex/
// Gemini after `blamely install` already ran": the tool's presence directory
// exists, but its config file has never been merged with the blamely hook.
// `blamely repair` should configure it without requiring a full reinstall.
func TestRepair_ConfiguresMissingToolHooks(t *testing.T) {
	home := fakeHomeDir(t)
	writeFakeInstalledBinary(t, home)

	// Gemini CLI "installed later": ~/.gemini exists (Detect finds it) but
	// settings.json doesn't exist yet, so its hook was never configured.
	if err := os.MkdirAll(filepath.Join(home, ".gemini"), 0o755); err != nil {
		t.Fatal(err)
	}

	result, err := Repair(false)
	if err != nil {
		t.Fatalf("Repair: %v", err)
	}
	if !hooksAddedContains(result, "Gemini") {
		t.Errorf("expected a Gemini hook to be configured, got %+v", result.HooksAdded)
	}
	if len(result.Errors) != 0 {
		t.Errorf("unexpected errors: %v", result.Errors)
	}

	settings := filepath.Join(home, ".gemini", "settings.json")
	data, err := os.ReadFile(settings)
	if err != nil {
		t.Fatalf("read gemini settings: %v", err)
	}
	if !strings.Contains(string(data), "blamely record gemini") {
		t.Errorf("gemini settings missing blamely hook: %s", data)
	}

	s, err := LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if !s.GeminiHookAdded {
		t.Error("expected GeminiHookAdded=true in persisted state")
	}

	// Re-running is a no-op: Install*Hook is idempotent, so nothing new is
	// reported the second time.
	result2, err := Repair(false)
	if err != nil {
		t.Fatalf("Repair (2nd run): %v", err)
	}
	if len(result2.HooksAdded) != 0 {
		t.Errorf("expected no hooks added on 2nd run, got %+v", result2.HooksAdded)
	}
}

// TestRepair_DryRun_DoesNotWriteHooks verifies --dry-run never touches tool
// config files, even when a tool is present and unconfigured.
func TestRepair_DryRun_DoesNotWriteHooks(t *testing.T) {
	home := fakeHomeDir(t)
	writeFakeInstalledBinary(t, home)

	if err := os.MkdirAll(filepath.Join(home, ".gemini"), 0o755); err != nil {
		t.Fatal(err)
	}

	result, err := Repair(true)
	if err != nil {
		t.Fatalf("Repair(dryRun): %v", err)
	}
	if len(result.HooksAdded) != 0 {
		t.Errorf("dry-run should not add hooks, got %+v", result.HooksAdded)
	}
	if _, err := os.Stat(filepath.Join(home, ".gemini", "settings.json")); err == nil {
		t.Error("dry-run should not create gemini settings.json")
	}
}

// TestRepair_NoBlamelyInstall_SkipsHookSetup verifies that when the stable
// binary doesn't exist yet (blamely was never installed), repair doesn't try
// to configure tool hooks against a binary path that doesn't exist —
// `blamely install` is the right command for a fresh setup.
func TestRepair_NoBlamelyInstall_SkipsHookSetup(t *testing.T) {
	home := fakeHomeDir(t)

	if err := os.MkdirAll(filepath.Join(home, ".gemini"), 0o755); err != nil {
		t.Fatal(err)
	}

	result, err := Repair(false)
	if err != nil {
		t.Fatalf("Repair: %v", err)
	}
	if len(result.HooksAdded) != 0 {
		t.Errorf("expected no hooks added without an installed binary, got %+v", result.HooksAdded)
	}
	if _, err := os.Stat(filepath.Join(home, ".gemini", "settings.json")); err == nil {
		t.Error("should not create gemini settings.json without an installed binary")
	}
}
