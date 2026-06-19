package tools

import (
	"encoding/json"
	"testing"
)

func TestExtractCopilotRanges_GenericFallback(t *testing.T) {
	// Copilot's actual payload schema isn't fully documented; the generic
	// fallback should still pick up {file_path, new_string} regardless of the
	// tool_name we get.
	raw, _ := json.Marshal(map[string]any{
		"file_path":  "/tmp/foo.go",
		"new_string": "line1\nline2\nline3\n",
	})
	p := copilotHookPayload{ToolName: "MysteryTool", ToolInput: raw}
	file, _, suggested, _, _ := extractCopilotRanges(p)
	if file != "/tmp/foo.go" {
		t.Errorf("file_path: want /tmp/foo.go, got %q", file)
	}
	if suggested != 3 {
		t.Errorf("suggested: want 3, got %d", suggested)
	}
}

func TestExtractCopilotRanges_EditShape_NarrowsToNewLinesOnly(t *testing.T) {
	// old_string "a" appears unchanged at the top of new_string. Only the
	// two genuinely-new lines ("b","c") should be credited to the AI, not
	// the unchanged context line ("a").
	raw, _ := json.Marshal(map[string]any{
		"file_path":  "/tmp/x.go",
		"old_string": "a",
		"new_string": "a\nb\nc",
	})
	p := copilotHookPayload{ToolName: "Edit", ToolInput: raw}
	file, _, suggested, _, _ := extractCopilotRanges(p)
	if file != "/tmp/x.go" {
		t.Errorf("want /tmp/x.go, got %q", file)
	}
	if suggested != 2 {
		t.Errorf("suggested: want 2 (b + c, not the unchanged 'a'), got %d", suggested)
	}
}

func TestExtractCopilotRanges_MultiEditSums(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"file_path": "/tmp/y.go",
		"edits": []map[string]any{
			{"old_string": "a", "new_string": "x\ny"}, // 2 lines
			{"old_string": "b", "new_string": "z"},    // 1 line
		},
	})
	p := copilotHookPayload{ToolName: "MultiEdit", ToolInput: raw}
	file, _, suggested, _, _ := extractCopilotRanges(p)
	if file != "/tmp/y.go" {
		t.Errorf("want /tmp/y.go, got %q", file)
	}
	if suggested != 3 {
		t.Errorf("suggested: want 3 (2+1), got %d", suggested)
	}
}

func TestExtractCopilotRanges_EditDeletion_CountsSuggested(t *testing.T) {
	// new_string is empty (deletion), old_string had 3 lines → suggested=3,
	// no ranges to locate (the text is gone post-edit).
	raw, _ := json.Marshal(map[string]any{
		"file_path":  "/tmp/d.go",
		"old_string": "a\nb\nc",
		"new_string": "",
	})
	p := copilotHookPayload{ToolName: "Edit", ToolInput: raw}
	file, ranges, suggested, _, _ := extractCopilotRanges(p)
	if file != "/tmp/d.go" {
		t.Errorf("file: want /tmp/d.go, got %q", file)
	}
	if ranges != nil {
		t.Errorf("ranges should be nil for pure deletion, got %+v", ranges)
	}
	if suggested != 3 {
		t.Errorf("suggested: want 3 (deleted lines), got %d", suggested)
	}
}

func TestExtractCopilotRanges_MultiEditMixedDeletion(t *testing.T) {
	// Two sub-edits: one adds 2 lines, one deletes 4 lines.
	raw, _ := json.Marshal(map[string]any{
		"file_path": "/tmp/m.go",
		"edits": []map[string]any{
			{"old_string": "x", "new_string": "x1\nx2"},                // +2
			{"old_string": "del1\ndel2\ndel3\ndel4", "new_string": ""}, // -4
		},
	})
	p := copilotHookPayload{ToolName: "MultiEdit", ToolInput: raw}
	_, _, suggested, _, _ := extractCopilotRanges(p)
	if suggested != 6 {
		t.Errorf("suggested: want 6 (2 added + 4 deleted), got %d", suggested)
	}
}

// A Copilot CLI deletion (str_replace_editor with old_str → empty new_str) must
// record removed-line hashes so the deletion attributes to copilot, not Human.
// Regression for commit 08c8b5b5 (CLI delete recorded lines=0 removed=0 → Human).
func TestExtractCopilotRanges_DeletionRecordsRemoved(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"path":    "/tmp/index.html",
		"old_str": "  <p>cimbom</p>\n  <p>Love you so much</p>",
		"new_str": "",
	})
	p := copilotHookPayload{ToolName: "str_replace_editor", ToolInput: raw}
	file, _, suggested, removed, _ := extractCopilotRanges(p)
	if file != "/tmp/index.html" {
		t.Fatalf("file: got %q", file)
	}
	if len(removed) != 2 {
		t.Fatalf("removed: want 2 deleted-line hashes, got %d (%+v)", len(removed), removed)
	}
	if suggested != 2 {
		t.Errorf("suggested: want 2, got %d", suggested)
	}
	// Edit shape too.
	raw2, _ := json.Marshal(map[string]any{"file_path": "/tmp/a.go", "old_string": "a\nb\nc", "new_string": ""})
	_, _, _, removed2, _ := extractCopilotRanges(copilotHookPayload{ToolName: "Edit", ToolInput: raw2})
	if len(removed2) != 3 {
		t.Fatalf("Edit deletion: want 3 removed, got %d", len(removed2))
	}
}

func TestExtractCopilotRanges_EmptyPayload(t *testing.T) {
	p := copilotHookPayload{ToolName: "Edit", ToolInput: json.RawMessage(`{}`)}
	file, ranges, suggested, _, _ := extractCopilotRanges(p)
	if file != "" || ranges != nil || suggested != 0 {
		t.Errorf("empty payload should return zero values, got (%q, %v, %d)", file, ranges, suggested)
	}
}

func TestCopilotGenType(t *testing.T) {
	cases := map[string]string{
		// The Copilot CLI hook's edits are command-line agent actions → "cli".
		"Edit":               "cli",
		"Write":              "cli",
		"Apply":              "cli",
		"str_replace_editor": "cli",
		"apply_patch":        "cli",
		// Explicit chat-panel tool names still map to chat.
		"chat-reply": "chat",
		"AskAgent":   "chat",
		"ChatPanel":  "chat",
	}
	for in, want := range cases {
		if got := copilotGenType(in); got != want {
			t.Errorf("copilotGenType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCountLines(t *testing.T) {
	cases := map[string]int{
		"":             0,
		"a":            1,
		"a\n":          1,
		"a\nb":         2,
		"a\nb\n":       2,
		"a\nb\nc":      3,
		"\n":           1, // empty line is still a line
		"a\nb\nc\nd\n": 4,
	}
	for in, want := range cases {
		if got := countLines(in); got != want {
			t.Errorf("countLines(%q) = %d, want %d", in, got, want)
		}
	}
}
