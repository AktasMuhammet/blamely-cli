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

// A Windows path in the delete directive is stored with JSON-escaped `\\`
// separators. The parser must recover the full native path — regressing to the
// old `[^"\\\n]+` class truncated it at the first backslash (to `C:`), so the
// deletion never matched the committed file and fell to Human.
func TestChatSessionDeletedFilesWindowsPath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.jsonl")
	content := `{"kind":2,"k":["requests",1,"response"],"v":[{"toolCalls":[{"name":"apply_patch","arguments":"{\"input\":\"*** Begin Patch\n*** Delete File: C:\\Users\\x\\repo\\login.html\n*** End Patch\"}"}]}]}` + "\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got := ChatSessionDeletedFiles(p)
	if len(got) != 1 || got[0] != `C:\Users\x\repo\login.html` {
		t.Fatalf(`want [C:\Users\x\repo\login.html], got %v`, got)
	}
}
