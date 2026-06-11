package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, body string) {
	t.Helper()
	// fakeHome (paths_test.go) sandboxes the home dir cross-platform — it sets
	// HOME *and* USERPROFILE, so os.UserHomeDir() resolves to our temp dir on
	// Windows too (where HOME alone is ignored).
	home := fakeHome(t)
	dir := filepath.Join(home, ".blamely")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfig_DefaultsWhenMissing(t *testing.T) {
	fakeHome(t)
	cfg := LoadConfig()
	if cfg != DefaultConfig() {
		t.Fatalf("missing config should yield defaults; got %+v", cfg)
	}
	n := cfg.Note
	if !(n.FileLines && n.Message && n.CodingTime && n.Tokens) {
		t.Fatalf("defaults should be all-on: %+v", n)
	}
	if n.Conversation {
		t.Fatalf("conversation should default off: %+v", n)
	}
}

func TestLoadConfig_PartialOverlayKeepsOtherDefaults(t *testing.T) {
	// Disabling one section must not turn off the rest.
	writeConfig(t, `{"note":{"conversation":false}}`)
	n := LoadConfig().Note
	if n.Conversation {
		t.Error("conversation should be disabled")
	}
	if !n.FileLines || !n.Message || !n.CodingTime || !n.Tokens {
		t.Errorf("unspecified sections must stay on: %+v", n)
	}
	// conversation_user defaults on; conversation_assistant defaults off.
	if !n.ConversationUser || n.ConversationAssistant {
		t.Errorf("conversation_user must default on, conversation_assistant off: %+v", n)
	}
}

func TestLoadConfig_PerRoleConversation(t *testing.T) {
	// Turn conversation on, drop the user (prompt) turns, and explicitly enable
	// assistant turns (which default off).
	writeConfig(t, `{"note":{"conversation":true,"conversation_user":false,"conversation_assistant":true}}`)
	n := LoadConfig().Note
	if !n.Conversation || n.ConversationUser || !n.ConversationAssistant {
		t.Fatalf("want conversation on, user off, assistant on: %+v", n)
	}
}

func TestLoadConfig_MalformedFallsBackToDefaults(t *testing.T) {
	writeConfig(t, `{not valid json`)
	if cfg := LoadConfig(); cfg != DefaultConfig() {
		t.Fatalf("malformed config must fall back to defaults; got %+v", cfg)
	}
}

func TestLoadConfig_AllDisabled(t *testing.T) {
	writeConfig(t, `{"note":{"file_lines":false,"conversation":false,"message":false,"coding_time":false,"tokens":false}}`)
	n := LoadConfig().Note
	if n.FileLines || n.Conversation || n.Message || n.CodingTime || n.Tokens {
		t.Fatalf("all sections should be off: %+v", n)
	}
}
