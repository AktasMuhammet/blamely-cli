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
// Every field defaults to true, so a missing config file — or one that lists
// only the toggles you want off — preserves full output. The note is always
// built in full and disabled sections are stripped just before it is written
// (see gitnotes.AttributeAndWrite), so turning a section off never changes how
// attribution itself is computed, only what is persisted.
type NoteConfig struct {
	// FileLines includes the per-line attribution detail inside each file
	// entry (the `lines` array). Turn off to keep only the per-file
	// +added/-deleted counts and the authorship totals — this is by far the
	// largest part of a note, so disabling it shrinks notes dramatically.
	FileLines bool `json:"file_lines"`
	// Conversation is the master switch for including any AI transcript turns
	// in the note. When false, no conversation is stored regardless of the
	// per-role toggles below.
	Conversation bool `json:"conversation"`
	// ConversationUser includes the USER (prompt) turns. Disable to keep the
	// assistant's replies but omit what the developer typed.
	ConversationUser bool `json:"conversation_user"`
	// ConversationAssistant includes the ASSISTANT (model reply) turns. Disable
	// to keep the user's prompts but omit the model's responses.
	ConversationAssistant bool `json:"conversation_assistant"`
	// Message includes the commit message in the note.
	Message bool `json:"message"`
	// CodingTime includes the coding_time_nanos field.
	CodingTime bool `json:"coding_time"`
	// Tokens includes token-usage counts (note totals + per-tool).
	Tokens bool `json:"tokens"`
}

// Config is the user-editable CLI configuration at ~/.blamely/config.json.
type Config struct {
	Note NoteConfig `json:"note"`
}

// DefaultConfig returns the all-enabled defaults, i.e. the historical behavior
// before config.json existed.
func DefaultConfig() Config {
	return Config{Note: NoteConfig{
		FileLines:             true,
		Conversation:          true,
		ConversationUser:      true,
		ConversationAssistant: true,
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

// EnsureDefaultConfigFile writes a default config.json (all sections on) if one
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
