package tools

import (
	"encoding/json"
	"testing"
)

// TestExtractCodexHookRanges_ClaudeCompatibleWriteReturnsFullContent verifies
// that a Claude-compatible "Write" tool call (some Codex versions emit these)
// passes through extractClaudeRanges' 5th return value — the full new file
// content, used as the snapshot-diff fallback's input since Write carries no
// "before" text of its own.
func TestExtractCodexHookRanges_ClaudeCompatibleWriteReturnsFullContent(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"file_path": "/tmp/w.go",
		"content":   "package main\n",
	})
	p := codexHookPayload{ToolName: "Write", ToolInput: raw}
	_, _, _, _, newFullContent := extractCodexHookRanges(p)
	if newFullContent == nil || *newFullContent != "package main\n" {
		t.Errorf("newFullContent: want %q, got %v", "package main\n", newFullContent)
	}
}

// TestExtractCodexHookRanges_ClaudeCompatibleEditReturnsNilFullContent
// verifies that a Claude-compatible "Edit" tool call (explicit before/after
// text) leaves the 5th return value nil — only Write needs the snapshot
// fallback.
func TestExtractCodexHookRanges_ClaudeCompatibleEditReturnsNilFullContent(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"file_path":  "/tmp/x.go",
		"old_string": "a\nb\nc\nd",
		"new_string": "",
	})
	p := codexHookPayload{ToolName: "Edit", ToolInput: raw}
	_, _, _, _, newFullContent := extractCodexHookRanges(p)
	if newFullContent != nil {
		t.Errorf("newFullContent: want nil, got %q", *newFullContent)
	}
}

// TestExtractCodexHookRanges_PatchReturnsNilFullContent verifies that the
// apply_patch path — which always provides a unified diff with explicit
// before/after content — leaves the 5th return value nil.
func TestExtractCodexHookRanges_PatchReturnsNilFullContent(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"input": "*** Begin Patch\n" +
			"*** Update File: /repo/main.go\n" +
			"@@\n" +
			"-old line\n" +
			"+new line\n" +
			"*** End Patch\n",
	})
	p := codexHookPayload{ToolName: "apply_patch", ToolInput: raw}
	filePath, _, _, _, newFullContent := extractCodexHookRanges(p)
	if filePath != "/repo/main.go" {
		t.Fatalf("filePath: want /repo/main.go, got %q", filePath)
	}
	if newFullContent != nil {
		t.Errorf("newFullContent: want nil, got %q", *newFullContent)
	}
}
