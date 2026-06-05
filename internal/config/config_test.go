package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, body string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".blamely")
	// t.TempDir() returns a fresh dir each call, so derive HOME from its parent.
	home := filepath.Dir(dir)
	t.Setenv("HOME", home)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfig_DefaultsWhenMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := LoadConfig()
	if cfg != DefaultConfig() {
		t.Fatalf("missing config should yield defaults; got %+v", cfg)
	}
	n := cfg.Note
	if !(n.FileLines && n.Conversation && n.Message && n.CodingTime && n.Tokens) {
		t.Fatalf("defaults should be all-on: %+v", n)
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
	// Per-role conversation toggles must also default on when not specified.
	if !n.ConversationUser || !n.ConversationAssistant {
		t.Errorf("per-role conversation toggles must default on: %+v", n)
	}
}

func TestLoadConfig_PerRoleConversation(t *testing.T) {
	// Keep conversation on but drop only the user (prompt) turns.
	writeConfig(t, `{"note":{"conversation_user":false}}`)
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
