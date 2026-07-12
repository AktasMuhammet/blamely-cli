package tools

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// Regression: Cursor's Windows hook runner pipes the PostToolUse JSON with a
// leading UTF-8 BOM, which json.Unmarshal rejects ("invalid character 'ï'") —
// silently dropping every hook-driven attribution on Windows and mislabeling
// chat applies as completion.
func TestReadHookPayload_StripsUTF8BOM(t *testing.T) {
	payload := `{"conversation_id":"93f364f3","model":"composer-2.5","tool_name":"Write","cursor_version":"3.10.20"}`
	raw, err := readHookPayload(bytes.NewReader(append([]byte{0xEF, 0xBB, 0xBF}, payload...)))
	if err != nil {
		t.Fatalf("readHookPayload: %v", err)
	}
	var p claudeHookPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("BOM-prefixed payload must parse, got: %v", err)
	}
	if p.CursorVersion != "3.10.20" || p.ConversationID != "93f364f3" {
		t.Errorf("parsed payload wrong: %+v", p)
	}
}

func TestReadHookPayload_NoBOMUnchanged(t *testing.T) {
	payload := `{"tool_name":"Write"}`
	raw, err := readHookPayload(strings.NewReader(payload))
	if err != nil {
		t.Fatalf("readHookPayload: %v", err)
	}
	if string(raw) != payload {
		t.Errorf("payload without BOM must pass through unchanged, got %q", raw)
	}
}
