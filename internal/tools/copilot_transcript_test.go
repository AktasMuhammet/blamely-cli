package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestParseApplyPatchPerLine(t *testing.T) {
	repo := gitInitRepo(t)
	target := filepath.Join(repo, "index.html")
	if err := os.WriteFile(target, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := "*** Begin Patch\n*** Update File: " + target + "\n@@\n" +
		"-  <p>old1</p>\n-  <p>old2</p>\n" +
		"+  <p>new1</p>\n+  <p>new2</p>\n+  <p>new3</p>\n" +
		"*** End Patch"
	files := parseApplyPatchPerLine(body)
	if len(files) != 1 {
		t.Fatalf("want 1 file, got %d", len(files))
	}
	f := files[0]
	if f.rel != "index.html" {
		t.Errorf("rel: got %q", f.rel)
	}
	if len(f.added) != 3 {
		t.Errorf("added: want 3, got %d", len(f.added))
	}
	if len(f.removed) != 2 {
		t.Errorf("removed: want 2, got %d", len(f.removed))
	}
	// per-line content_sha present + matches the daemon convention
	if f.added[0].ContentSHA != sha256Hex([]byte("  <p>new1</p>")) {
		t.Errorf("added[0] content_sha mismatch")
	}
	if f.removed[0].ContentSHA != sha256Hex([]byte("  <p>old1</p>")) {
		t.Errorf("removed[0] content_sha mismatch")
	}
}

// emit path: arguments double-encoded as a JSON string wrapping {"input": body}.
func TestEmitCopilotPatchEdits_DoubleEncodedArgs(t *testing.T) {
	repo := gitInitRepo(t)
	target := filepath.Join(repo, "a.go")
	if err := os.WriteFile(target, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := "*** Begin Patch\n*** Update File: " + target + "\n@@\n-old\n+new\n*** End Patch"
	inner, _ := json.Marshal(map[string]string{"input": body})
	args, _ := json.Marshal(string(inner)) // double-encode (JSON string literal)

	sink := &mockSink{}
	emitCopilotPatchEdits(json.RawMessage(args), "gpt-5-mini", 100, 42, "/tmp/t.jsonl", sink)
	if len(sink.events) != 1 {
		t.Fatalf("want 1 emitted edit, got %d", len(sink.events))
	}
	e := sink.events[0]
	if e.Tool != "copilot" || e.GenType != "chat" || e.Model != "gpt-5-mini" {
		t.Errorf("event meta wrong: %+v", e)
	}
	if e.FilePath != "a.go" || len(e.Lines) != 1 || len(e.RemovedLines) != 1 {
		t.Errorf("event content wrong: file=%s added=%d removed=%d", e.FilePath, len(e.Lines), len(e.RemovedLines))
	}
	if e.OutputTokens == nil || *e.OutputTokens != 42 {
		t.Errorf("output tokens not set")
	}
	if e.InputTokens == nil || *e.InputTokens != 100 {
		t.Errorf("input tokens not set")
	}
}
