package install

import (
	"os"
	"path/filepath"
	"testing"
)

func setupFakeCodexHome(t *testing.T, content string) string {
	t.Helper()
	home := fakeHomeDir(t)
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if content != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func TestInstallCodexHook_EmptyFile(t *testing.T) {
	setupFakeCodexHome(t, "")
	added, configPath, err := InstallCodexHook("/usr/local/bin/blamely")
	if err != nil {
		t.Fatalf("InstallCodexHook: %v", err)
	}
	if !added {
		t.Error("expected added=true")
	}
	// Re-parse the file and verify structure.
	root, err := readTOML(configPath)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	features, _ := root["features"].(map[string]any)
	if features == nil || features["hooks"] != true {
		t.Errorf("features.hooks=true missing, got: %#v", features)
	}
	hooks, _ := root["hooks"].(map[string]any)
	post, _ := hooks["PostToolUse"].([]any)
	if len(post) != 1 {
		t.Fatalf("expected 1 PostToolUse group, got %d", len(post))
	}
	grp, _ := post[0].(map[string]any)
	inner, _ := grp["hooks"].([]any)
	if len(inner) != 1 {
		t.Fatalf("expected 1 inner hook, got %d", len(inner))
	}
	hm, _ := inner[0].(map[string]any)
	if cmd, _ := hm["command"].(string); cmd != "/usr/local/bin/blamely record codex" {
		t.Errorf("unexpected command: %q", cmd)
	}
}

func TestInstallCodexHook_Idempotent(t *testing.T) {
	setupFakeCodexHome(t, "")
	if _, _, err := InstallCodexHook("/bin/blamely"); err != nil {
		t.Fatal(err)
	}
	added, _, err := InstallCodexHook("/bin/blamely")
	if err != nil {
		t.Fatal(err)
	}
	if added {
		t.Error("expected added=false on second install")
	}
}

func TestInstallCodexHook_PreservesExistingHooks(t *testing.T) {
	existing := `[features]
hooks = true

[[hooks.PostToolUse]]

[[hooks.PostToolUse.hooks]]
command = "/Users/x/.git-ai/bin/git-ai checkpoint codex --hook-input stdin"
type = "command"

[hooks.state."/Users/x/.codex/config.toml:post_tool_use:0:0"]
enabled = true
trusted_hash = "sha256:deadbeef"
`
	setupFakeCodexHome(t, existing)
	added, configPath, err := InstallCodexHook("/bin/blamely")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !added {
		t.Error("expected added=true")
	}
	root, err := readTOML(configPath)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	hooks, _ := root["hooks"].(map[string]any)
	post, _ := hooks["PostToolUse"].([]any)
	if len(post) != 2 {
		t.Fatalf("expected 2 groups (git-ai + blamely), got %d", len(post))
	}
	// hooks.state.* must survive
	state, _ := hooks["state"].(map[string]any)
	if state == nil {
		t.Fatal("hooks.state was dropped")
	}
}

func TestUninstallCodexHook_RemovesOurs_KeepsOthers(t *testing.T) {
	existing := `[features]
hooks = true

[[hooks.PostToolUse]]

[[hooks.PostToolUse.hooks]]
command = "/Users/x/.git-ai/bin/git-ai checkpoint codex --hook-input stdin"
type = "command"

[[hooks.PostToolUse]]

[[hooks.PostToolUse.hooks]]
command = "/bin/blamely record codex"
type = "command"
`
	setupFakeCodexHome(t, existing)
	removed, err := UninstallCodexHook()
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Error("expected removed=true")
	}
	home, _ := os.UserHomeDir()
	root, err := readTOML(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	hooks, _ := root["hooks"].(map[string]any)
	post, _ := hooks["PostToolUse"].([]any)
	if len(post) != 1 {
		t.Fatalf("expected 1 group after uninstall, got %d", len(post))
	}
	grp, _ := post[0].(map[string]any)
	inner, _ := grp["hooks"].([]any)
	hm, _ := inner[0].(map[string]any)
	if cmd, _ := hm["command"].(string); !containsSubstr(cmd, "git-ai") {
		t.Errorf("git-ai hook was lost, got %q", cmd)
	}
}

func TestUninstallCodexHook_NoOp_WhenAbsent(t *testing.T) {
	setupFakeCodexHome(t, "")
	removed, err := UninstallCodexHook()
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Error("expected removed=false")
	}
}
