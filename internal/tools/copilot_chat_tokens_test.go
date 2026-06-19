package tools

import (
	"os"
	"path/filepath"
	"testing"
)

// Current VS Code chat shape: per-request tokens live in result.metadata as
// promptTokens/outputTokens (not the legacy result.usage.completionTokens).
func TestChatSessionUsage_MetadataShape(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.jsonl")
	lines := `{"kind":0,"v":{"requests":[{}],"inputState":{"selectedModel":"gpt-5-mini"}}}
{"kind":1,"k":["requests",0,"result"],"v":{"metadata":{"promptTokens":21568,"outputTokens":78}}}
{"kind":2,"k":["requests"],"v":[{}]}
{"kind":1,"k":["requests",1,"result"],"v":{"metadata":{"promptTokens":20872,"outputTokens":910}}}`
	if err := os.WriteFile(p, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}

	// Per-request usage.
	u0, _ := ReadChatSessionRequestUsage(p, 0)
	if u0 == nil || u0.InputTokens != 21568 || u0.OutputTokens != 78 {
		t.Fatalf("req0 usage wrong: %+v", u0)
	}
	u1, _ := ReadChatSessionRequestUsage(p, 1)
	if u1 == nil || u1.InputTokens != 20872 || u1.OutputTokens != 910 {
		t.Fatalf("req1 usage wrong: %+v", u1)
	}
	// Latest usage = last request with tokens.
	l, _ := ReadChatSessionLatestUsage(p)
	if l == nil || l.OutputTokens != 910 {
		t.Fatalf("latest usage wrong: %+v", l)
	}
}

// Legacy result.usage shape still parses (back-compat).
func TestChatSessionUsage_LegacyUsageShape(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.jsonl")
	lines := `{"kind":0,"v":{"requests":[{}]}}
{"kind":1,"k":["requests",0,"result"],"v":{"usage":{"promptTokens":100,"completionTokens":40}}}`
	if err := os.WriteFile(p, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	u, _ := ReadChatSessionRequestUsage(p, 0)
	if u == nil || u.InputTokens != 100 || u.OutputTokens != 40 {
		t.Fatalf("legacy usage not parsed: %+v", u)
	}
}
