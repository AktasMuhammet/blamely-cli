package tools

import (
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
