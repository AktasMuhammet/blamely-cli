package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReadGeminiTranscriptUsage_UsageMetadata(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "transcript.jsonl")
	content := `{"usageMetadata":{"promptTokenCount":200,"candidatesTokenCount":40,"cachedContentTokenCount":15}}
{"llm_response":{"usageMetadata":{"promptTokenCount":300,"candidatesTokenCount":60}}}
`
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := ReadGeminiTranscriptUsage(p)
	if err != nil {
		t.Fatal(err)
	}
	if u == nil {
		t.Fatal("expected usage")
	}
	if u.InputTokens != 300 || u.OutputTokens != 60 {
		t.Errorf("want latest 300/60, got %d/%d", u.InputTokens, u.OutputTokens)
	}
	if u.CacheReadTokens != 0 {
		// second line has no cache field
	}
}

// TestExtractGeminiRanges_WriteFileReturnsFullContent verifies that
// write_file's 5th return value carries the full new file content —
// write_file overwrites the whole file with no "before" text of its own, so
// RecordGeminiFromStdin uses this to fetch the daemon's cached snapshot and
// compute removed-line hashes.
func TestExtractGeminiRanges_WriteFileReturnsFullContent(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"file_path": "/tmp/does-not-exist.go",
		"content":   "package main\n",
	})
	p := geminiHookPayload{ToolName: "write_file", ToolInput: raw}
	_, _, _, _, newFullContent := extractGeminiRanges(p)
	if newFullContent == nil || *newFullContent != "package main\n" {
		t.Errorf("newFullContent: want %q, got %v", "package main\n", newFullContent)
	}
}

// TestExtractGeminiRanges_ReplaceReturnsNilFullContent verifies that replace
// (which carries explicit before/after text of its own) leaves the 5th
// return value nil — only write_file needs the snapshot fallback.
func TestExtractGeminiRanges_ReplaceReturnsNilFullContent(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"file_path":  "/tmp/x.go",
		"old_string": "a\nb\nc",
		"new_string": "",
	})
	p := geminiHookPayload{ToolName: "replace", ToolInput: raw}
	_, _, _, _, newFullContent := extractGeminiRanges(p)
	if newFullContent != nil {
		t.Errorf("newFullContent: want nil, got %q", *newFullContent)
	}
}
