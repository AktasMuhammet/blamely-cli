package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const configFileName = "config.json"

// NoteConfig toggles which sections the post-commit git note includes.
//
// All fields default to true except Conversation and ConversationAssistant
// (both off by default), so a missing config file — or one that lists only
// the toggles you want to flip — preserves that baseline. The note is always
// built in full and disabled sections are stripped just before it is written
// (see gitnotes.AttributeAndWrite), so turning a section off never changes
// how attribution itself is computed, only what is persisted.
type NoteConfig struct {
	// FileLines includes the per-line attribution detail inside each file
	// entry (the `lines` array). Turn off to keep only the per-file
	// +added/-deleted counts and the authorship totals — this is by far the
	// largest part of a note, so disabling it shrinks notes dramatically.
	FileLines bool `json:"file_lines"`
	// Conversation is the master switch for including any AI transcript turns
	// in the note. Defaults to false — transcripts may contain sensitive
	// prompt/response content the user hasn't opted to persist into git
	// notes. When false, no conversation is stored regardless of the
	// per-role toggles below.
	Conversation bool `json:"conversation"`
	// ConversationUser includes the USER (prompt) turns. Disable to keep the
	// assistant's replies but omit what the developer typed.
	ConversationUser bool `json:"conversation_user"`
	// ConversationAssistant includes the ASSISTANT (model reply) turns. Defaults
	// to false — assistant replies are typically long and add little value to
	// the note; enable to keep the model's responses alongside the user's prompts.
	ConversationAssistant bool `json:"conversation_assistant"`
	// Message includes the commit message in the note.
	Message bool `json:"message"`
	// CodingTime includes the coding_time_nanos field.
	CodingTime bool `json:"coding_time"`
	// Tokens includes token-usage counts (note totals + per-tool).
	Tokens bool `json:"tokens"`
}

// ToolsConfig records EXTRA AI-tool config directories to watch, for setups where
// Codex/Claude are run with a non-default home (corporate provisioning). These are
// purely ADDITIVE: the standard ~/.codex and ~/.claude (and the CODEX_HOME /
// CLAUDE_CONFIG_DIR env dirs) are ALWAYS scanned too — a dir listed here is added to
// that set, never a replacement. The daemon runs under launchd/systemd/schtasks and
// does not inherit the user's shell env, so `blamely install` captures the env dirs
// into here and users/admins can add more with `blamely config add tools.codex_home`.
type ToolsConfig struct {
	// CodexHomes are extra Codex home dirs (each contains sessions/ + config.toml),
	// added to ~/.codex and $CODEX_HOME.
	CodexHomes []string `json:"codex_homes,omitempty"`
	// ClaudeConfigDirs are extra Claude config dirs (each contains projects/ +
	// settings.json), added to ~/. and $CLAUDE_CONFIG_DIR.
	ClaudeConfigDirs []string `json:"claude_config_dirs,omitempty"`
}

// Config is the user-editable CLI configuration at ~/.blamely/config.json.
type Config struct {
	Note  NoteConfig  `json:"note"`
	Tools ToolsConfig `json:"tools"`
}

// DefaultConfig returns the default note settings: everything on except
// Conversation and ConversationAssistant, which are opt-in.
func DefaultConfig() Config {
	return Config{Note: NoteConfig{
		FileLines:             true,
		Conversation:          false,
		ConversationUser:      true,
		ConversationAssistant: false,
		Message:               true,
		CodingTime:            true,
		Tokens:                true,
	}}
}

// ConfigFile returns the path to ~/.blamely/config.json.
func ConfigFile() (string, error) {
	d, err := BlamelyDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, configFileName), nil
}

// LoadConfig reads ~/.blamely/config.json layered over the defaults.
//
// Any failure — missing file, unreadable path, malformed JSON — yields the
// full-output defaults. A broken config must never silently strip a user's
// note data or break the post-commit hook. Because the file is unmarshaled
// ON TOP of the defaults, only the keys actually present are overridden, so a
// user can write just `{"note":{"conversation":false}}` to disable one section
// while everything else stays on.
func LoadConfig() Config {
	cfg := DefaultConfig()
	path, err := ConfigFile()
	if err != nil {
		return cfg
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(data, &cfg)
	return cfg
}

// NoteKeys returns the settable note toggles in a stable display order. The
// canonical form is dotted (e.g. "note.file_lines"); the `config` CLI also
// accepts the bare suffix ("file_lines").
func NoteKeys() []string {
	return []string{
		"note.file_lines",
		"note.conversation",
		"note.conversation_user",
		"note.conversation_assistant",
		"note.message",
		"note.coding_time",
		"note.tokens",
	}
}

// normKey lowercases, trims, and drops an optional "note." prefix so callers
// may pass either "note.file_lines" or "file_lines".
func normKey(key string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(key)), "note.")
}

// GetBool returns the value of a note toggle by key. ok=false for unknown keys.
func (c Config) GetBool(key string) (val bool, ok bool) {
	switch normKey(key) {
	case "file_lines":
		return c.Note.FileLines, true
	case "conversation":
		return c.Note.Conversation, true
	case "conversation_user":
		return c.Note.ConversationUser, true
	case "conversation_assistant":
		return c.Note.ConversationAssistant, true
	case "message":
		return c.Note.Message, true
	case "coding_time":
		return c.Note.CodingTime, true
	case "tokens":
		return c.Note.Tokens, true
	}
	return false, false
}

// SetBool sets a note toggle by key. ok=false for unknown keys.
func (c *Config) SetBool(key string, val bool) (ok bool) {
	switch normKey(key) {
	case "file_lines":
		c.Note.FileLines = val
	case "conversation":
		c.Note.Conversation = val
	case "conversation_user":
		c.Note.ConversationUser = val
	case "conversation_assistant":
		c.Note.ConversationAssistant = val
	case "message":
		c.Note.Message = val
	case "coding_time":
		c.Note.CodingTime = val
	case "tokens":
		c.Note.Tokens = val
	default:
		return false
	}
	return true
}

// ListKeys returns the settable list-valued keys (extra tool dirs) in display
// order. The canonical form is dotted; a bare/singular suffix is also accepted
// (see normListKey), so `blamely config add tools.codex_home <path>` works.
func ListKeys() []string {
	return []string{
		"tools.codex_homes",
		"tools.claude_config_dirs",
	}
}

// normListKey canonicalises a list key: lowercases, trims, drops an optional
// "tools." prefix, and folds the singular spelling to the plural field name so
// both `tools.codex_home` and `tools.codex_homes` resolve to CodexHomes.
func normListKey(key string) string {
	k := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(key)), "tools.")
	switch k {
	case "codex_home", "codex_homes":
		return "codex_homes"
	case "claude_config_dir", "claude_config_dirs":
		return "claude_config_dirs"
	}
	return k
}

// CanonicalListKey maps a user-supplied list key (bare/singular/dotted, any case)
// to its canonical dotted form for display. Falls back to the input if unknown.
func CanonicalListKey(userKey string) string {
	switch normListKey(userKey) {
	case "codex_homes":
		return "tools.codex_homes"
	case "claude_config_dirs":
		return "tools.claude_config_dirs"
	}
	return userKey
}

// GetList returns the values of a list key. ok=false for unknown keys.
func (c Config) GetList(key string) (val []string, ok bool) {
	switch normListKey(key) {
	case "codex_homes":
		return c.Tools.CodexHomes, true
	case "claude_config_dirs":
		return c.Tools.ClaudeConfigDirs, true
	}
	return nil, false
}

// AddToList appends a value to a list key if not already present. Returns
// ok=false for unknown keys, added=false when the value was already there.
func (c *Config) AddToList(key, value string) (added, ok bool) {
	v := strings.TrimSpace(value)
	if v == "" {
		return false, true
	}
	appendUnique := func(list *[]string) (bool, bool) {
		for _, e := range *list {
			if e == v {
				return false, true
			}
		}
		*list = append(*list, v)
		return true, true
	}
	switch normListKey(key) {
	case "codex_homes":
		return appendUnique(&c.Tools.CodexHomes)
	case "claude_config_dirs":
		return appendUnique(&c.Tools.ClaudeConfigDirs)
	}
	return false, false
}

// RemoveFromList drops a value from a list key. Returns ok=false for unknown
// keys, removed=false when the value was not present.
func (c *Config) RemoveFromList(key, value string) (removed, ok bool) {
	v := strings.TrimSpace(value)
	dropVal := func(list *[]string) (bool, bool) {
		out := (*list)[:0]
		found := false
		for _, e := range *list {
			if e == v {
				found = true
				continue
			}
			out = append(out, e)
		}
		*list = out
		return found, true
	}
	switch normListKey(key) {
	case "codex_homes":
		return dropVal(&c.Tools.CodexHomes)
	case "claude_config_dirs":
		return dropVal(&c.Tools.ClaudeConfigDirs)
	}
	return false, false
}

// CaptureToolDirsFromEnv persists any custom Codex/Claude home set in the current
// environment ($CODEX_HOME / $CLAUDE_CONFIG_DIR) into config.Tools, but ONLY when it
// differs from the home default — the default is always scanned, so recording it
// would be redundant. This is how the daemon (which does not inherit the installing
// shell's env) learns to watch a corporate tool dir. Idempotent: AddToList dedupes,
// and it writes only when something changed.
func CaptureToolDirsFromEnv() error {
	home, _ := Home()
	cfg := LoadConfig()
	changed := false
	if v := strings.TrimSpace(os.Getenv("CODEX_HOME")); v != "" {
		if home == "" || filepath.Clean(v) != filepath.Join(home, ".codex") {
			if added, _ := cfg.AddToList("codex_homes", v); added {
				changed = true
			}
		}
	}
	if v := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); v != "" {
		if home == "" || filepath.Clean(v) != filepath.Join(home, ".claude") {
			if added, _ := cfg.AddToList("claude_config_dirs", v); added {
				changed = true
			}
		}
	}
	if !changed {
		return nil
	}
	_, err := SaveConfig(cfg)
	return err
}

// ParseBoolValue accepts the usual true/false plus friendly on/off and yes/no
// spellings so `blamely config set … on` works as users expect.
func ParseBoolValue(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "yes", "y", "enable", "enabled":
		return true, nil
	case "off", "no", "n", "disable", "disabled":
		return false, nil
	}
	return strconv.ParseBool(strings.TrimSpace(s))
}

// SaveConfig writes the config to ~/.blamely/config.json (pretty-printed,
// creating ~/.blamely if needed) and returns the path it wrote.
func SaveConfig(c Config) (string, error) {
	path, err := ConfigFile()
	if err != nil {
		return "", err
	}
	if _, err := EnsureBlamelyDir(); err != nil {
		return "", err
	}
	body, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// EnsureDefaultConfigFile writes a default config.json (DefaultConfig) if one
// does not already exist, so the available toggles are discoverable after
// install. It NEVER overwrites an existing file — users edit it to customise
// note output, and a re-install must keep their edits intact.
func EnsureDefaultConfigFile() (path string, created bool, err error) {
	path, err = ConfigFile()
	if err != nil {
		return "", false, err
	}
	if _, statErr := os.Stat(path); statErr == nil {
		return path, false, nil
	} else if !os.IsNotExist(statErr) {
		return "", false, statErr
	}
	if _, err = EnsureBlamelyDir(); err != nil {
		return "", false, err
	}
	body, err := json.MarshalIndent(DefaultConfig(), "", "  ")
	if err != nil {
		return "", false, err
	}
	if err = os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
		return "", false, err
	}
	return path, true, nil
}
