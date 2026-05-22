package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func cursorHooksAt(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cursor hooks: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse cursor hooks: %v", err)
	}
	return m
}

func setupFakeCursorHome(t *testing.T, content string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".cursor")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if content != "" {
		if err := os.WriteFile(filepath.Join(dir, "hooks.json"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func TestInstallCursorHook_EmptyFile(t *testing.T) {
	setupFakeCursorHome(t, "")
	added, hooksPath, err := InstallCursorHook("/usr/local/bin/blamely")
	if err != nil {
		t.Fatalf("InstallCursorHook: %v", err)
	}
	if !added {
		t.Error("expected added=true")
	}
	m := cursorHooksAt(t, hooksPath)
	if m["version"] == nil {
		t.Error("version key missing")
	}
	hooks, _ := m["hooks"].(map[string]any)
	post, _ := hooks["postToolUse"].([]any)
	if len(post) != 1 {
		t.Fatalf("expected 1 postToolUse entry, got %d", len(post))
	}
	entry, _ := post[0].(map[string]any)
	if cmd, _ := entry["command"].(string); cmd != "/usr/local/bin/blamely record cursor" {
		t.Errorf("unexpected command: %q", cmd)
	}
}

func TestInstallCursorHook_Idempotent(t *testing.T) {
	setupFakeCursorHome(t, "")
	if _, _, err := InstallCursorHook("/bin/blamely"); err != nil {
		t.Fatal(err)
	}
	added, _, err := InstallCursorHook("/bin/blamely")
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if added {
		t.Error("expected added=false on second install")
	}
}

func TestInstallCursorHook_PreservesExisting(t *testing.T) {
	existing := `{
  "version": 1,
  "hooks": {
    "postToolUse": [
      { "command": "/Users/x/.git-ai/bin/git-ai checkpoint cursor --hook-input stdin" }
    ],
    "preToolUse": [
      { "command": "/Users/x/.git-ai/bin/git-ai checkpoint cursor --hook-input stdin" }
    ]
  }
}`
	setupFakeCursorHome(t, existing)
	added, hooksPath, err := InstallCursorHook("/bin/blamely")
	if err != nil {
		t.Fatal(err)
	}
	if !added {
		t.Error("expected added=true")
	}
	m := cursorHooksAt(t, hooksPath)
	hooks, _ := m["hooks"].(map[string]any)
	if _, ok := hooks["preToolUse"]; !ok {
		t.Error("preToolUse was dropped")
	}
	post, _ := hooks["postToolUse"].([]any)
	if len(post) != 2 {
		t.Errorf("expected 2 postToolUse entries, got %d", len(post))
	}
}

func TestUninstallCursorHook_RemovesOurs_KeepsOthers(t *testing.T) {
	existing := `{
  "version": 1,
  "hooks": {
    "postToolUse": [
      { "command": "/Users/x/.git-ai/bin/git-ai checkpoint cursor --hook-input stdin" },
      { "command": "/bin/blamely record cursor" }
    ]
  }
}`
	setupFakeCursorHome(t, existing)
	removed, err := UninstallCursorHook()
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Error("expected removed=true")
	}
	home, _ := os.UserHomeDir()
	m := cursorHooksAt(t, filepath.Join(home, ".cursor", "hooks.json"))
	hooks, _ := m["hooks"].(map[string]any)
	post, _ := hooks["postToolUse"].([]any)
	if len(post) != 1 {
		t.Fatalf("expected 1 entry after uninstall, got %d", len(post))
	}
	entry, _ := post[0].(map[string]any)
	if cmd, _ := entry["command"].(string); !containsSubstr(cmd, "git-ai") {
		t.Errorf("git-ai hook was removed, got %q", cmd)
	}
}

func TestUninstallCursorHook_NoOp_WhenAbsent(t *testing.T) {
	setupFakeCursorHome(t, "")
	removed, err := UninstallCursorHook()
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Error("expected removed=false")
	}
}
