package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readHooks returns settings.json's hooks map.
func readHooks(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	hooks, _ := root["hooks"].(map[string]any)
	return hooks
}

// hasBlamelyIn reports whether the event's groups contain a blamely command.
func hasBlamelyIn(hooks map[string]any, event string) bool {
	groups, _ := hooks[event].([]any)
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		inner, _ := gm["hooks"].([]any)
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			if cmd, _ := hm["command"].(string); strings.Contains(cmd, blamelyHookMarker) {
				return true
			}
		}
	}
	return false
}

// Install must converge on claudeHookEvents whichever build wrote the file. An
// event a PREVIOUS build registered and the current one does not must be removed:
// a reinstall never visits it, so a leftover hook would keep spawning a process on
// every tool call forever.
func TestInstallClaudeHook_DropsEventsNoLongerRegistered(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")

	// A settings.json as an older build left it: blamely on PreToolUse too.
	seed := `{
  "theme": "dark",
  "hooks": {
    "PostToolUse": [
      {"matcher": "Write|Edit", "hooks": [{"command": "/old/blamely record claude", "type": "command"}]}
    ],
    "PreToolUse": [
      {"matcher": "Write|Edit", "hooks": [{"command": "/old/blamely record claude --pre", "type": "command"}]}
    ]
  }
}`
	if err := os.WriteFile(settings, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := installClaudeHookAt(settings, "/new/blamely"); err != nil {
		t.Fatalf("installClaudeHookAt: %v", err)
	}

	hooks := readHooks(t, settings)
	for _, event := range claudeHookEvents {
		if !hasBlamelyIn(hooks, event) {
			t.Errorf("event %q: blamely hook missing after install", event)
		}
	}
	// Every OTHER event must be free of our hook.
	for event := range hooks {
		var registered bool
		for _, e := range claudeHookEvents {
			if e == event {
				registered = true
			}
		}
		if !registered && hasBlamelyIn(hooks, event) {
			t.Errorf("event %q is not in claudeHookEvents but still holds a blamely hook", event)
		}
	}
	// Unrelated settings survive.
	data, _ := os.ReadFile(settings)
	if !strings.Contains(string(data), `"theme"`) {
		t.Error("unrelated settings were dropped")
	}
	// The stale binary path is gone.
	if strings.Contains(string(data), "/old/blamely") {
		t.Error("stale binary path still present")
	}
}

// A third party's hook on an event we don't register must survive the strip.
func TestInstallClaudeHook_KeepsForeignHooks(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	seed := `{
  "hooks": {
    "PreToolUse": [
      {"matcher": "*", "hooks": [{"command": "/opt/other-tool/hook", "type": "command"}]}
    ]
  }
}`
	if err := os.WriteFile(settings, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := installClaudeHookAt(settings, "/new/blamely"); err != nil {
		t.Fatalf("installClaudeHookAt: %v", err)
	}
	data, _ := os.ReadFile(settings)
	if !strings.Contains(string(data), "/opt/other-tool/hook") {
		t.Error("a foreign hook on an unregistered event was removed")
	}
}
