package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// fakeHomeConf redirects config.Home() to a temp dir.
func fakeHomeConf(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

// ── DefaultConfig ─────────────────────────────────────────────────────────────

func TestDefaultConfig_AllTrue(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Note.FileLines || !cfg.Note.Conversation || !cfg.Note.ConversationUser ||
		!cfg.Note.Message || !cfg.Note.CodingTime || !cfg.Note.Tokens {
		t.Error("DefaultConfig should have all note toggles = true except conversation_assistant")
	}
	if cfg.Note.ConversationAssistant {
		t.Error("DefaultConfig should have conversation_assistant = false")
	}
}

// ── NoteKeys ──────────────────────────────────────────────────────────────────

func TestNoteKeys_Canonical(t *testing.T) {
	keys := NoteKeys()
	for _, k := range keys {
		cfg := DefaultConfig()
		if _, ok := cfg.GetBool(k); !ok {
			t.Errorf("NoteKeys returned unknown key %q", k)
		}
	}
}

// ── GetBool / SetBool ─────────────────────────────────────────────────────────

func TestGetBool_AllKeys(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		{"note.file_lines", true},
		{"file_lines", true},            // bare suffix accepted
		{"note.conversation", true},
		{"note.conversation_user", true},
		{"note.conversation_assistant", false},
		{"note.message", true},
		{"note.coding_time", true},
		{"note.tokens", true},
		{"  NOTE.FILE_LINES  ", true},   // case+whitespace tolerant
	}
	cfg := DefaultConfig()
	for _, c := range cases {
		val, ok := cfg.GetBool(c.key)
		if !ok {
			t.Errorf("GetBool(%q): want ok=true", c.key)
		}
		if val != c.want {
			t.Errorf("GetBool(%q): want %v, got %v", c.key, c.want, val)
		}
	}
}

func TestGetBool_UnknownKey(t *testing.T) {
	cfg := DefaultConfig()
	_, ok := cfg.GetBool("note.nonexistent")
	if ok {
		t.Error("GetBool with unknown key should return ok=false")
	}
}

func TestSetBool_AllKeys(t *testing.T) {
	cfg := DefaultConfig()
	keys := NoteKeys()
	for _, k := range keys {
		ok := cfg.SetBool(k, false)
		if !ok {
			t.Errorf("SetBool(%q, false): want ok=true", k)
		}
		val, _ := cfg.GetBool(k)
		if val != false {
			t.Errorf("after SetBool(%q, false): GetBool returned true", k)
		}
		cfg.SetBool(k, true) // reset
	}
}

func TestSetBool_UnknownKey(t *testing.T) {
	cfg := DefaultConfig()
	ok := cfg.SetBool("note.bogus", true)
	if ok {
		t.Error("SetBool with unknown key should return ok=false")
	}
}

func TestSetBool_BareSuffix(t *testing.T) {
	cfg := DefaultConfig()
	ok := cfg.SetBool("tokens", false)
	if !ok {
		t.Error("bare suffix 'tokens' should be accepted by SetBool")
	}
	if cfg.Note.Tokens {
		t.Error("SetBool(tokens, false) should set Tokens=false")
	}
}

// ── ParseBoolValue ────────────────────────────────────────────────────────────

func TestParseBoolValue(t *testing.T) {
	trueInputs := []string{"true", "True", "TRUE", "1", "on", "ON", "yes", "YES", "y", "Y", "enable", "ENABLE", "enabled", "ENABLED"}
	falseInputs := []string{"false", "False", "FALSE", "0", "off", "OFF", "no", "NO", "n", "N", "disable", "DISABLE", "disabled", "DISABLED"}
	for _, s := range trueInputs {
		v, err := ParseBoolValue(s)
		if err != nil || !v {
			t.Errorf("ParseBoolValue(%q): want true/nil, got %v/%v", s, v, err)
		}
	}
	for _, s := range falseInputs {
		v, err := ParseBoolValue(s)
		if err != nil || v {
			t.Errorf("ParseBoolValue(%q): want false/nil, got %v/%v", s, v, err)
		}
	}
}

func TestParseBoolValue_Whitespace(t *testing.T) {
	v, err := ParseBoolValue("  true  ")
	if err != nil || !v {
		t.Errorf("whitespace-trimmed 'true': got %v/%v", v, err)
	}
}

func TestParseBoolValue_Invalid(t *testing.T) {
	_, err := ParseBoolValue("maybe")
	if err == nil {
		t.Error("ParseBoolValue('maybe') should return an error")
	}
}

// ── SaveConfig / LoadConfig round-trip ───────────────────────────────────────

func TestSaveAndLoadConfig_RoundTrip(t *testing.T) {
	fakeHomeConf(t)
	cfg := DefaultConfig()
	cfg.Note.Conversation = false
	cfg.Note.Tokens = false

	path, err := SaveConfig(cfg)
	if err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if path == "" {
		t.Error("SaveConfig returned empty path")
	}

	loaded := LoadConfig()
	if loaded.Note.Conversation != false {
		t.Error("LoadConfig: Conversation should be false after save")
	}
	if loaded.Note.Tokens != false {
		t.Error("LoadConfig: Tokens should be false after save")
	}
	// Unmodified keys stay true.
	if !loaded.Note.FileLines {
		t.Error("LoadConfig: FileLines should still be true")
	}
}

func TestSaveConfig_WritesValidJSON(t *testing.T) {
	fakeHomeConf(t)
	cfg := DefaultConfig()
	path, err := SaveConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	var out Config
	if err := json.Unmarshal(data, &out); err != nil {
		t.Errorf("saved config is not valid JSON: %v", err)
	}
}

// ── EnsureDefaultConfigFile ───────────────────────────────────────────────────

func TestEnsureDefaultConfigFile_CreatesWhenMissing(t *testing.T) {
	fakeHomeConf(t)
	path, created, err := EnsureDefaultConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("EnsureDefaultConfigFile: want created=true on first call")
	}
	if path == "" {
		t.Error("returned empty path")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("config file was not created at %s: %v", path, err)
	}
}

func TestEnsureDefaultConfigFile_DoesNotOverwrite(t *testing.T) {
	fakeHomeConf(t)
	// First call creates the file.
	path, _, _ := EnsureDefaultConfigFile()

	// Write custom content.
	custom := []byte(`{"note":{"tokens":false}}`)
	if err := os.WriteFile(path, custom, 0o644); err != nil {
		t.Fatal(err)
	}

	// Second call must NOT overwrite.
	_, created, err := EnsureDefaultConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("EnsureDefaultConfigFile: want created=false when file already exists")
	}
	data, _ := os.ReadFile(path)
	if string(data) != string(custom) {
		t.Error("EnsureDefaultConfigFile overwrote existing file")
	}
}

// ── ExcludeList ───────────────────────────────────────────────────────────────

func TestParseExclude_IgnoresCommentsAndBlanks(t *testing.T) {
	content := `
# comment
*.log

vendor/

`
	el := parseExclude(content)
	if el.Patterns() != 2 {
		t.Errorf("want 2 patterns, got %d", el.Patterns())
	}
}

func TestExcludeList_MatchGlob(t *testing.T) {
	content := "*.log\nnode_modules/\n"
	el := parseExclude(content)

	cases := []struct {
		path  string
		match bool
	}{
		{"app.log", true},
		{"logs/error.log", true},   // basename glob matches anywhere
		{"app.go", false},
		{"node_modules/react/index.js", true}, // directory prefix
		{"src/index.js", false},
	}
	for _, c := range cases {
		if got := el.Match(c.path); got != c.match {
			t.Errorf("Match(%q): want %v, got %v", c.path, c.match, got)
		}
	}
}

func TestExcludeList_MatchLiteral(t *testing.T) {
	el := parseExclude("dist/bundle.js\n")
	if !el.Match("dist/bundle.js") {
		t.Error("exact path should match")
	}
	if el.Match("dist/bundle2.js") {
		t.Error("non-matching path should not match")
	}
}

func TestExcludeList_Empty(t *testing.T) {
	el := parseExclude("")
	if el.Match("any/file.go") {
		t.Error("empty exclude list should match nothing")
	}
	if el.Patterns() != 0 {
		t.Errorf("empty list: want 0 patterns, got %d", el.Patterns())
	}
}

func TestLoadExcludeListFrom_MissingFile(t *testing.T) {
	_, err := LoadExcludeListFrom(filepath.Join(t.TempDir(), "nonexistent"))
	if err == nil {
		t.Fatal("LoadExcludeListFrom should return an error for a missing file")
	}
}

func TestLoadExcludeListFrom_ValidFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".blamely-exclude")
	os.WriteFile(p, []byte("*.min.js\nbuild/\n"), 0o644) //nolint
	el, err := LoadExcludeListFrom(p)
	if err != nil {
		t.Fatal(err)
	}
	if el.Patterns() != 2 {
		t.Errorf("want 2 patterns, got %d", el.Patterns())
	}
	if !el.Match("app.min.js") {
		t.Error("*.min.js should match app.min.js")
	}
}
