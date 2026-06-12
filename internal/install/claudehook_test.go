package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// claudeSettingsAt reads back the settings JSON at path and returns the raw map.
func claudeSettingsAt(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse settings: %v", err)
	}
	return m
}

// setupFakeHome creates a temp HOME with ~/.claude/settings.json pre-seeded
// with the given JSON content (empty string = no settings.json).
func setupFakeHome(t *testing.T, content string) string {
	t.Helper()
	home := fakeHomeDir(t)
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if content != "" {
		if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func TestInstallClaudeHook_EmptySettings(t *testing.T) {
	setupFakeHome(t, "")
	added, settingsPath, err := InstallClaudeHook("/usr/local/bin/blamely")
	if err != nil {
		t.Fatalf("InstallClaudeHook: %v", err)
	}
	if !added {
		t.Error("expected added=true on first install")
	}
	m := claudeSettingsAt(t, settingsPath)
	if m["hooks"] == nil {
		t.Fatal("hooks key missing after install")
	}
	// Verify our hook is present
	if !containsBlamelyHook("/usr/local/bin/blamely record claude") {
		t.Error("containsBlamelyHook helper broken")
	}
	if !groupsHaveBlamelyHook(getSlice(getMap(m, "hooks", false), "PostToolUse")) {
		t.Error("hook not found in installed settings")
	}
}

// groupsHaveBlamelyHook reports whether any matcher group's hooks contain a
// blamely command. Test helper (the production alreadyPresent check was
// replaced by strip-then-prepend reconcile).
func groupsHaveBlamelyHook(groups []any) bool {
	for _, g := range groups {
		grp, _ := g.(map[string]any)
		for _, h := range getSlice(grp, "hooks") {
			hm, _ := h.(map[string]any)
			if cmd, _ := hm["command"].(string); containsBlamelyHook(cmd) {
				return true
			}
		}
	}
	return false
}

func TestInstallClaudeHook_Idempotent(t *testing.T) {
	setupFakeHome(t, "")
	_, _, err := InstallClaudeHook("/usr/bin/blamely")
	if err != nil {
		t.Fatal(err)
	}
	// Second install should report added=false
	added, _, err := InstallClaudeHook("/usr/bin/blamely")
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if added {
		t.Error("expected added=false on second install (idempotent)")
	}
}

func TestInstallClaudeHook_ExistingHooks_Preserved(t *testing.T) {
	existing := `{
  "model": "claude-opus-4-7",
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Bash",
        "hooks": [{"type":"command","command":"echo user-hook"}]
      }
    ]
  }
}`
	setupFakeHome(t, existing)
	added, settingsPath, err := InstallClaudeHook("/bin/blamely")
	if err != nil {
		t.Fatalf("InstallClaudeHook: %v", err)
	}
	if !added {
		t.Error("expected added=true")
	}
	m := claudeSettingsAt(t, settingsPath)

	// model key must still be present
	if m["model"] != "claude-opus-4-7" {
		t.Errorf("model key lost: got %v", m["model"])
	}
	hooks := getMap(m, "hooks", false)
	groups := getSlice(hooks, "PostToolUse")
	if len(groups) < 2 {
		t.Fatalf("expected ≥2 PostToolUse groups after install, got %d", len(groups))
	}
	// The original Bash hook must still be there
	bashFound := false
	for _, g := range groups {
		grp, _ := g.(map[string]any)
		if grp["matcher"] == "Bash" {
			bashFound = true
		}
	}
	if !bashFound {
		t.Error("original Bash matcher was removed")
	}
}

func TestInstallClaudeHook_MergesIntoExistingMatcher(t *testing.T) {
	// If a PostToolUse group with our matcher already exists (but without our hook),
	// our hook should be appended inside that group rather than creating a new group.
	existing := `{
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Write|Edit|MultiEdit|NotebookEdit|Bash",
        "hooks": [{"type":"command","command":"echo other"}]
      }
    ]
  }
}`
	setupFakeHome(t, existing)
	_, settingsPath, err := InstallClaudeHook("/bin/blamely")
	if err != nil {
		t.Fatal(err)
	}
	m := claudeSettingsAt(t, settingsPath)
	groups := getSlice(getMap(m, "hooks", false), "PostToolUse")
	if len(groups) != 1 {
		t.Errorf("expected exactly 1 group (merged), got %d", len(groups))
	}
	grp, _ := groups[0].(map[string]any)
	inner := getSlice(grp, "hooks")
	if len(inner) != 2 {
		t.Errorf("expected 2 hooks in merged group (other + blamely), got %d", len(inner))
	}
}

// TestInstallClaudeHook_RunsFirst reproduces the reported bug: a third-party
// tool's hook is already configured for PostToolUse. blamely must install its
// hook FIRST in the chain so that a failing earlier hook can't abort the chain
// before blamely records the edit.
func TestInstallClaudeHook_RunsFirst(t *testing.T) {
	existing := `{
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Write",
        "hooks": [{"type":"command","command":"/some/other-tool hook"}]
      }
    ]
  }
}`
	setupFakeHome(t, existing)
	_, settingsPath, err := InstallClaudeHook("/bin/blamely")
	if err != nil {
		t.Fatal(err)
	}
	m := claudeSettingsAt(t, settingsPath)
	groups := getSlice(getMap(m, "hooks", false), "PostToolUse")
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups (blamely + other), got %d", len(groups))
	}
	// blamely's group must be first.
	first, _ := groups[0].(map[string]any)
	inner := getSlice(first, "hooks")
	if len(inner) == 0 {
		t.Fatal("first group has no hooks")
	}
	hm, _ := inner[0].(map[string]any)
	cmd, _ := hm["command"].(string)
	if !containsBlamelyHook(cmd) {
		t.Errorf("expected blamely hook first, got %q (full groups: %v)", cmd, groups)
	}
}

// TestInstallClaudeHook_RunsFirst_SharedMatcher: when a foreign hook already
// occupies blamely's exact matcher group, blamely's command must be prepended
// inside that group (runs before the foreign command), not appended after it.
func TestInstallClaudeHook_RunsFirst_SharedMatcher(t *testing.T) {
	existing := `{
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Write|Edit|MultiEdit|NotebookEdit|Bash",
        "hooks": [{"type":"command","command":"/some/other-tool hook"}]
      }
    ]
  }
}`
	setupFakeHome(t, existing)
	_, settingsPath, err := InstallClaudeHook("/bin/blamely")
	if err != nil {
		t.Fatal(err)
	}
	m := claudeSettingsAt(t, settingsPath)
	groups := getSlice(getMap(m, "hooks", false), "PostToolUse")
	if len(groups) != 1 {
		t.Fatalf("expected 1 merged group, got %d", len(groups))
	}
	grp, _ := groups[0].(map[string]any)
	inner := getSlice(grp, "hooks")
	if len(inner) != 2 {
		t.Fatalf("expected 2 hooks in merged group, got %d", len(inner))
	}
	hm, _ := inner[0].(map[string]any)
	if cmd, _ := hm["command"].(string); !containsBlamelyHook(cmd) {
		t.Errorf("expected blamely hook first inside shared matcher, got %q", cmd)
	}
}

// TestInstallClaudeHook_DedupesAndReplaces reproduces the reported bug: an
// existing config has DUPLICATE blamely hooks pointing at a STALE binary path
// (from older/buggy installs). Re-running install must collapse them to a
// single fresh hook (current path) kept first, while preserving foreign hooks.
func TestInstallClaudeHook_DedupesAndReplaces(t *testing.T) {
	existing := `{
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Write",
        "hooks": [{"type":"command","command":"/some/other-tool hook"}]
      },
      {
        "matcher": "Write|Edit|MultiEdit|NotebookEdit|Bash",
        "hooks": [
          {"type":"command","command":"/old/path/blamely record claude"},
          {"type":"command","command":"/old/path/blamely record claude"}
        ]
      }
    ]
  }
}`
	setupFakeHome(t, existing)
	added, settingsPath, err := InstallClaudeHook("/new/bin/blamely")
	if err != nil {
		t.Fatal(err)
	}
	if !added {
		t.Error("expected added=true (duplicates collapsed + stale path replaced)")
	}
	m := claudeSettingsAt(t, settingsPath)
	groups := getSlice(getMap(m, "hooks", false), "PostToolUse")

	// Exactly one blamely hook must survive, and it must be the very first hook.
	count, foreign := 0, false
	var firstCmd string
	for gi, g := range groups {
		grp, _ := g.(map[string]any)
		for hi, h := range getSlice(grp, "hooks") {
			hm, _ := h.(map[string]any)
			cmd, _ := hm["command"].(string)
			if containsBlamelyHook(cmd) {
				count++
				if gi == 0 && hi == 0 {
					firstCmd = cmd
				}
			}
			if cmd == "/some/other-tool hook" {
				foreign = true
			}
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 blamely hook after dedupe, got %d", count)
	}
	if firstCmd != "/new/bin/blamely record claude" {
		t.Errorf("blamely hook should be first and use the new path, got firstCmd=%q", firstCmd)
	}
	if !foreign {
		t.Error("foreign hook was dropped — must be preserved")
	}

	// And it's now idempotent: a second identical install rewrites nothing.
	added2, _, err := InstallClaudeHook("/new/bin/blamely")
	if err != nil {
		t.Fatal(err)
	}
	if added2 {
		t.Error("expected added=false on second install after dedupe")
	}
}

func TestInstallClaudeHook_MigratesLegacyMatcher(t *testing.T) {
	// A blamely hook installed by an older version used a matcher without Bash.
	// Re-running install must upgrade that group's matcher in place (not add a
	// duplicate hook).
	existing := `{
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Write|Edit|MultiEdit|NotebookEdit",
        "hooks": [{"type":"command","command":"/bin/blamely record claude"}]
      }
    ]
  }
}`
	setupFakeHome(t, existing)
	added, settingsPath, err := InstallClaudeHook("/bin/blamely")
	if err != nil {
		t.Fatal(err)
	}
	if !added {
		t.Error("expected added=true (matcher migrated)")
	}
	m := claudeSettingsAt(t, settingsPath)
	groups := getSlice(getMap(m, "hooks", false), "PostToolUse")
	if len(groups) != 1 {
		t.Fatalf("expected 1 group (migrated in place), got %d", len(groups))
	}
	grp, _ := groups[0].(map[string]any)
	if grp["matcher"] != claudeHookMatcher {
		t.Errorf("matcher not migrated: got %v want %v", grp["matcher"], claudeHookMatcher)
	}
	if inner := getSlice(grp, "hooks"); len(inner) != 1 {
		t.Errorf("expected hook not duplicated, got %d", len(inner))
	}
}

func TestUninstallClaudeHook_RemovesOurHook(t *testing.T) {
	setupFakeHome(t, "")
	_, _, err := InstallClaudeHook("/bin/blamely")
	if err != nil {
		t.Fatal(err)
	}
	removed, err := UninstallClaudeHook()
	if err != nil {
		t.Fatalf("UninstallClaudeHook: %v", err)
	}
	if !removed {
		t.Error("expected removed=true")
	}
}

func TestUninstallClaudeHook_PreservesOtherHooks(t *testing.T) {
	existing := `{
  "model": "claude-opus-4-7",
  "hooks": {
    "PostToolUse": [
      {"matcher":"Bash","hooks":[{"type":"command","command":"echo user"}]},
      {"matcher":"Write|Edit|MultiEdit|NotebookEdit","hooks":[{"type":"command","command":"/bin/blamely record claude"}]}
    ]
  }
}`
	setupFakeHome(t, existing)
	removed, err := UninstallClaudeHook()
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Error("expected removed=true")
	}

	// settings.json should still exist with the Bash hook and model key intact.
	home, _ := os.UserHomeDir()
	m := claudeSettingsAt(t, filepath.Join(home, ".claude", "settings.json"))
	if m["model"] != "claude-opus-4-7" {
		t.Errorf("model key lost during uninstall: %v", m["model"])
	}
	groups := getSlice(getMap(m, "hooks", false), "PostToolUse")
	for _, g := range groups {
		grp, _ := g.(map[string]any)
		for _, h := range getSlice(grp, "hooks") {
			hm, _ := h.(map[string]any)
			if containsBlamelyHook(hm["command"].(string)) {
				t.Error("blamely hook still present after uninstall")
			}
		}
	}
	// Bash hook must survive
	bashFound := false
	for _, g := range groups {
		grp, _ := g.(map[string]any)
		if grp["matcher"] == "Bash" {
			bashFound = true
		}
	}
	if !bashFound {
		t.Error("Bash hook was removed during uninstall")
	}
}

func TestUninstallClaudeHook_NoOp_WhenNotInstalled(t *testing.T) {
	setupFakeHome(t, `{"model":"claude-opus-4-7"}`)
	removed, err := UninstallClaudeHook()
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Error("expected removed=false when hook was never installed")
	}
}

func TestUninstallClaudeHook_NoOp_WhenFileAbsent(t *testing.T) {
	setupFakeHome(t, "") // no settings.json created
	removed, err := UninstallClaudeHook()
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Error("expected removed=false when settings.json doesn't exist")
	}
}

func TestContainsBlamelyHook(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		{"/usr/local/bin/blamely record claude", true},
		{"/home/user/.blamely/bin/blamely record claude", true},
		{"blamely record claude", true},
		{`C:\Users\me\.blamely\bin\blamely.exe record claude`, true},
		{"echo hello", false},
		{"blamely status", false},
		{"blamely record cursor", false},
		{"", false},
	}
	for _, c := range cases {
		if got := containsBlamelyHook(c.cmd); got != c.want {
			t.Errorf("containsBlamelyHook(%q) = %v, want %v", c.cmd, got, c.want)
		}
	}
}
