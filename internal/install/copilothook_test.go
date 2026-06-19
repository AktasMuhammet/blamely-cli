package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func setupFakeCopilotHome(t *testing.T, content string) string {
	t.Helper()
	home := fakeHomeDir(t)
	hooksDir := filepath.Join(home, ".copilot", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if content != "" {
		if err := os.WriteFile(filepath.Join(hooksDir, "blamely.json"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func copilotHookAt(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read copilot hook: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse copilot hook: %v", err)
	}
	return m
}

func TestInstallCopilotHook_CreatesFile(t *testing.T) {
	setupFakeCopilotHome(t, "")
	added, hookPath, err := InstallCopilotHook("/usr/local/bin/blamely")
	if err != nil {
		t.Fatalf("InstallCopilotHook: %v", err)
	}
	if !added {
		t.Error("expected added=true")
	}
	m := copilotHookAt(t, hookPath)
	hooks, _ := m["hooks"].(map[string]any)
	post, _ := hooks["PostToolUse"].([]any)
	if len(post) != 1 {
		t.Fatalf("expected 1 PostToolUse entry, got %d", len(post))
	}
	entry, _ := post[0].(map[string]any)
	if cmd, _ := entry["command"].(string); cmd != "/usr/local/bin/blamely record copilot" {
		t.Errorf("unexpected command: %q", cmd)
	}
	if typ, _ := entry["type"].(string); typ != "command" {
		t.Errorf("type missing/wrong: %q", typ)
	}
}

func TestInstallCopilotHook_DoesNotTouchOtherToolsFile(t *testing.T) {
	home := setupFakeCopilotHome(t, "")
	// Drop a sibling other-tool hook file that should be left alone.
	otherToolPath := filepath.Join(home, ".copilot", "hooks", "other-tool.json")
	otherToolContent := `{"hooks":{"PostToolUse":[{"command":"/Users/x/.other-tool/bin/other-tool checkpoint github-copilot --hook-input stdin","type":"command"}]}}`
	if err := os.WriteFile(otherToolPath, []byte(otherToolContent), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := InstallCopilotHook("/bin/blamely"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(otherToolPath)
	if err != nil {
		t.Fatalf("other-tool file disappeared: %v", err)
	}
	if string(data) != otherToolContent {
		t.Errorf("other-tool file was modified:\nwant: %s\ngot:  %s", otherToolContent, data)
	}
}

func TestInstallCopilotHook_Idempotent(t *testing.T) {
	setupFakeCopilotHome(t, "")
	if _, _, err := InstallCopilotHook("/bin/blamely"); err != nil {
		t.Fatal(err)
	}
	added, _, err := InstallCopilotHook("/bin/blamely")
	if err != nil {
		t.Fatal(err)
	}
	if added {
		t.Error("expected added=false on second install")
	}
}

func TestUninstallCopilotHook_DeletesOurFile(t *testing.T) {
	setupFakeCopilotHome(t, "")
	if _, _, err := InstallCopilotHook("/bin/blamely"); err != nil {
		t.Fatal(err)
	}
	removed, err := UninstallCopilotHook()
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Error("expected removed=true")
	}
	home, _ := os.UserHomeDir()
	if _, err := os.Stat(filepath.Join(home, ".copilot", "hooks", "blamely.json")); !os.IsNotExist(err) {
		t.Errorf("blamely.json should have been deleted, stat err: %v", err)
	}
}

func TestUninstallCopilotHook_NoOp_WhenAbsent(t *testing.T) {
	setupFakeCopilotHome(t, "")
	removed, err := UninstallCopilotHook()
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Error("expected removed=false")
	}
}
