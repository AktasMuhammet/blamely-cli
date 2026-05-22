package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func geminiSettingsAt(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read gemini settings: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse gemini settings: %v", err)
	}
	return m
}

func setupFakeGeminiHome(t *testing.T, content string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if content != "" {
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func TestInstallGeminiHook_EmptyFile(t *testing.T) {
	setupFakeGeminiHome(t, "")
	added, settingsPath, err := InstallGeminiHook("/usr/local/bin/blamely")
	if err != nil {
		t.Fatalf("InstallGeminiHook: %v", err)
	}
	if !added {
		t.Error("expected added=true")
	}
	m := geminiSettingsAt(t, settingsPath)
	tools, _ := m["tools"].(map[string]any)
	if tools == nil || tools["enableHooks"] != true {
		t.Errorf("tools.enableHooks not set: %#v", tools)
	}
	hooks, _ := m["hooks"].(map[string]any)
	after, _ := hooks["AfterTool"].([]any)
	if len(after) != 1 {
		t.Fatalf("expected 1 AfterTool group, got %d", len(after))
	}
	grp, _ := after[0].(map[string]any)
	if matcher, _ := grp["matcher"].(string); matcher != "*" {
		t.Errorf("unexpected matcher: %q", matcher)
	}
}

func TestInstallGeminiHook_Idempotent(t *testing.T) {
	setupFakeGeminiHome(t, "")
	if _, _, err := InstallGeminiHook("/bin/blamely"); err != nil {
		t.Fatal(err)
	}
	added, _, err := InstallGeminiHook("/bin/blamely")
	if err != nil {
		t.Fatal(err)
	}
	if added {
		t.Error("expected added=false on second install")
	}
}

func TestInstallGeminiHook_PreservesExisting(t *testing.T) {
	existing := `{
  "tools": { "enableHooks": true },
  "hooks": {
    "AfterTool": [
      {
        "matcher": "*",
        "hooks": [{"type":"command","command":"/Users/x/.git-ai/bin/git-ai checkpoint gemini --hook-input stdin"}]
      }
    ],
    "BeforeTool": [
      {
        "matcher": "*",
        "hooks": [{"type":"command","command":"/Users/x/.git-ai/bin/git-ai checkpoint gemini --hook-input stdin"}]
      }
    ]
  }
}`
	setupFakeGeminiHome(t, existing)
	added, settingsPath, err := InstallGeminiHook("/bin/blamely")
	if err != nil {
		t.Fatal(err)
	}
	if !added {
		t.Error("expected added=true")
	}
	m := geminiSettingsAt(t, settingsPath)
	hooks, _ := m["hooks"].(map[string]any)

	// BeforeTool must survive untouched.
	if _, ok := hooks["BeforeTool"]; !ok {
		t.Error("BeforeTool was dropped")
	}

	// Our hook should be merged into the existing "*" matcher group.
	after, _ := hooks["AfterTool"].([]any)
	if len(after) != 1 {
		t.Fatalf("expected 1 AfterTool group (merged), got %d", len(after))
	}
	grp, _ := after[0].(map[string]any)
	inner, _ := grp["hooks"].([]any)
	if len(inner) != 2 {
		t.Errorf("expected 2 hooks inside the matcher (git-ai + blamely), got %d", len(inner))
	}
}

func TestUninstallGeminiHook_RemovesOurs_KeepsOthers(t *testing.T) {
	existing := `{
  "tools": { "enableHooks": true },
  "hooks": {
    "AfterTool": [
      {
        "matcher": "*",
        "hooks": [
          {"type":"command","command":"/Users/x/.git-ai/bin/git-ai checkpoint gemini --hook-input stdin"},
          {"type":"command","command":"/bin/blamely record gemini"}
        ]
      }
    ]
  }
}`
	setupFakeGeminiHome(t, existing)
	removed, err := UninstallGeminiHook()
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Error("expected removed=true")
	}
	home, _ := os.UserHomeDir()
	m := geminiSettingsAt(t, filepath.Join(home, ".gemini", "settings.json"))
	hooks, _ := m["hooks"].(map[string]any)
	after, _ := hooks["AfterTool"].([]any)
	if len(after) != 1 {
		t.Fatalf("expected 1 group, got %d", len(after))
	}
	grp, _ := after[0].(map[string]any)
	inner, _ := grp["hooks"].([]any)
	if len(inner) != 1 {
		t.Fatalf("expected 1 remaining hook, got %d", len(inner))
	}
	hm, _ := inner[0].(map[string]any)
	if cmd, _ := hm["command"].(string); !containsSubstr(cmd, "git-ai") {
		t.Errorf("git-ai hook was lost: %q", cmd)
	}
}

func TestUninstallGeminiHook_NoOp_WhenAbsent(t *testing.T) {
	setupFakeGeminiHome(t, "")
	removed, err := UninstallGeminiHook()
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Error("expected removed=false")
	}
}
