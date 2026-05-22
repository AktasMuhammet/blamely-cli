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
	file, _, suggested := extractCopilotRanges(p)
	if file != "/tmp/foo.go" {
		t.Errorf("file_path: want /tmp/foo.go, got %q", file)
	}
	if suggested != 3 {
		t.Errorf("suggested: want 3, got %d", suggested)
	}
}

func TestExtractCopilotRanges_EditShape(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"file_path":  "/tmp/x.go",
		"old_string": "a",
		"new_string": "a\nb\nc",
	})
	p := copilotHookPayload{ToolName: "Edit", ToolInput: raw}
	file, _, suggested := extractCopilotRanges(p)
	if file != "/tmp/x.go" {
		t.Errorf("want /tmp/x.go, got %q", file)
	}
	if suggested != 3 {
		t.Errorf("suggested: want 3, got %d", suggested)
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
	file, _, suggested := extractCopilotRanges(p)
	if file != "/tmp/y.go" {
		t.Errorf("want /tmp/y.go, got %q", file)
	}
	if suggested != 3 {
		t.Errorf("suggested: want 3 (2+1), got %d", suggested)
	}
}

func TestExtractCopilotRanges_EmptyPayload(t *testing.T) {
	p := copilotHookPayload{ToolName: "Edit", ToolInput: json.RawMessage(`{}`)}
	file, ranges, suggested := extractCopilotRanges(p)
	if file != "" || ranges != nil || suggested != 0 {
		t.Errorf("empty payload should return zero values, got (%q, %v, %d)", file, ranges, suggested)
	}
}

func TestCopilotGenType(t *testing.T) {
	cases := map[string]string{
		"Edit":       "completion",
		"Write":      "completion",
		"chat-reply": "chat",
		"AskAgent":   "chat",
		"ChatPanel":  "chat",
		"Apply":      "completion",
	}
	for in, want := range cases {
		if got := copilotGenType(in); got != want {
			t.Errorf("copilotGenType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCountLines(t *testing.T) {
	cases := map[string]int{
		"":            0,
		"a":           1,
		"a\n":         1,
		"a\nb":        2,
		"a\nb\n":      2,
		"a\nb\nc":     3,
		"\n":          1, // empty line is still a line
		"a\nb\nc\nd\n": 4,
	}
	for in, want := range cases {
		if got := countLines(in); got != want {
			t.Errorf("countLines(%q) = %d, want %d", in, got, want)
		}
	}
}
