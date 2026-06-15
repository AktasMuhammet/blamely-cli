package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestChatSessionDeletedFiles(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.jsonl")
	// One apply_patch delete (JSON-escaped, as Copilot writes it), one unrelated line.
	content := `{"kind":2,"k":["requests"],"v":[{"response":[{"value":"ok"}]}]}` + "\n" +
		`{"kind":2,"k":["requests",1,"response"],"v":[{"toolCalls":[{"name":"apply_patch","arguments":"{\"input\":\"*** Begin Patch\n*** Delete File: /Users/x/repo/hotel-reservation.html\n*** End Patch\"}"}]}]}` + "\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got := ChatSessionDeletedFiles(p)
	if len(got) != 1 || got[0] != "/Users/x/repo/hotel-reservation.html" {
		t.Fatalf("want [/Users/x/repo/hotel-reservation.html], got %v", got)
	}
}
