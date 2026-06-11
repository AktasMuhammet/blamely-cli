package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/blamely/blamely/internal/store"
)

func TestParseCodeActionEdit(t *testing.T) {
	content := "Created At: 2026-06-07T21:00:59Z\nCompleted At: 2026-06-07T21:01:01Z\nThe following changes were made by the replace_file_content tool to: /Users/abdulkerimatik/development/training/gemini-test/index.html. If relevant, proactively run terminal commands to execute this code for the USER. Don't ask for permission.\n[diff_block_start]\n@@ -135,6 +135,16 @@\n           </svg>\n           GitHub\n         </button>\n+        <button type=\"button\" class=\"social-btn\" id=\"ldap-login-btn\">\n+          <svg></svg>\n+          LDAP\n+        </button>\n       </div>\n \n       <!-- Sign Up Prompt -->\n[diff_block_end]\n\nPlease note..."

	path, ranges, suggested, wholeFile, removed := parseCodeAction(content)
	if wholeFile {
		t.Fatalf("expected edit (not wholeFile)")
	}
	if path != "/Users/abdulkerimatik/development/training/gemini-test/index.html" {
		t.Fatalf("path = %q", path)
	}
	if suggested != 4 {
		t.Fatalf("suggested = %d, want 4", suggested)
	}
	wantStarts := []int{138, 139, 140, 141}
	if len(ranges) != len(wantStarts) {
		t.Fatalf("ranges = %+v", ranges)
	}
	for i, r := range ranges {
		if r.Start != wantStarts[i] || r.End != wantStarts[i] {
			t.Errorf("range[%d] = %+v, want start/end %d", i, r, wantStarts[i])
		}
	}
	if len(removed) != 0 {
		t.Errorf("removed = %+v, want none (diff has no removed lines)", removed)
	}
}

func TestParseCodeActionCreate(t *testing.T) {
	content := "Created At: 2026-06-07T18:22:53Z\nCompleted At: 2026-06-07T18:22:55Z\nCreated file file:///Users/abdulkerimatik/development/training/gemini-test/style.css with requested content.\nIf relevant, proactively run terminal commands to execute this code for the USER. Don't ask for permission."

	path, ranges, _, wholeFile, _ := parseCodeAction(content)
	if !wholeFile {
		t.Fatalf("expected wholeFile=true")
	}
	if path != "/Users/abdulkerimatik/development/training/gemini-test/style.css" {
		t.Fatalf("path = %q", path)
	}
	if ranges != nil {
		t.Fatalf("expected nil ranges for wholeFile, got %+v", ranges)
	}
}

func TestAntigravityGeminiWatcher_EditEmitsChatEdit(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@l"}, {"config", "user.name", "T"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if err := cmd.Run(); err != nil {
			t.Skipf("git not available: %v", err)
		}
	}
	target := filepath.Join(repo, "index.html")
	if err := os.WriteFile(target, []byte("<html>\n<body>\n</body>\n</html>\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	logDir := filepath.Join(root, "447f1fc3-conv", ".system_generated", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(logDir, "transcript.jsonl")

	step := map[string]any{
		"step_index": 15,
		"source":     "MODEL",
		"type":       "CODE_ACTION",
		"status":     "DONE",
		"created_at": "2026-06-07T21:00:59Z",
		"content": "The following changes were made by the replace_file_content tool to: " + target +
			". If relevant, proactively run terminal commands.\n" +
			"[diff_block_start]\n@@ -1,4 +1,5 @@\n <html>\n+<!-- added by gemini -->\n <body>\n </body>\n </html>\n[diff_block_end]\n",
	}
	line, err := json.Marshal(step)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, append(line, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	sink := &captureSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	w := &antigravityTranscriptWatcher{roots: []string{root}}
	_ = w.run(ctx, sink)

	if len(sink.events) != 1 {
		t.Fatalf("events = %d, want 1: %+v", len(sink.events), sink.events)
	}
	ev := sink.events[0]
	if ev.Tool != string(store.ToolGemini) || ev.GenType != "chat" || ev.Confidence != "high" {
		t.Errorf("tool=%q gen_type=%q confidence=%q, want gemini/chat/high", ev.Tool, ev.GenType, ev.Confidence)
	}
	if ev.FilePath != "index.html" {
		t.Errorf("file_path = %q, want index.html", ev.FilePath)
	}
	if len(ev.Lines) != 1 || ev.Lines[0].Start != 2 || ev.Lines[0].End != 2 {
		t.Errorf("lines = %+v, want single range at line 2", ev.Lines)
	}
	if ev.SuggestedLines != 1 {
		t.Errorf("suggested_lines = %d, want 1", ev.SuggestedLines)
	}
}

func TestAntigravityGeminiWatcher_CreateEmitsWholeFileChatEdit(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@l"}, {"config", "user.name", "T"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if err := cmd.Run(); err != nil {
			t.Skipf("git not available: %v", err)
		}
	}
	target := filepath.Join(repo, "style.css")
	if err := os.WriteFile(target, []byte("body { margin: 0; }\n.btn { color: red; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	logDir := filepath.Join(root, "5fd1e50e-conv", ".system_generated", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(logDir, "transcript.jsonl")

	step := map[string]any{
		"step_index": 7,
		"source":     "MODEL",
		"type":       "CODE_ACTION",
		"status":     "DONE",
		"created_at": "2026-06-07T18:22:53Z",
		"content":    "Created file file://" + target + " with requested content.\nIf relevant, proactively run terminal commands.",
	}
	line, err := json.Marshal(step)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, append(line, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	sink := &captureSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	w := &antigravityTranscriptWatcher{roots: []string{root}}
	_ = w.run(ctx, sink)

	if len(sink.events) != 1 {
		t.Fatalf("events = %d, want 1: %+v", len(sink.events), sink.events)
	}
	ev := sink.events[0]
	if ev.FilePath != "style.css" {
		t.Errorf("file_path = %q, want style.css", ev.FilePath)
	}
	// One single-line range per line (each with its own content SHA), not one
	// combined whole-file range — so per-line content_sha attribution can match.
	if len(ev.Lines) != 2 {
		t.Fatalf("lines = %+v, want 2 single-line ranges", ev.Lines)
	}
	for i := range ev.Lines {
		if ev.Lines[i].Start != i+1 || ev.Lines[i].End != i+1 {
			t.Errorf("lines[%d] = %+v, want %d..%d", i, ev.Lines[i], i+1, i+1)
		}
	}
	if ev.SuggestedLines != 2 {
		t.Errorf("suggested_lines = %d, want 2", ev.SuggestedLines)
	}
}

func TestConversationDBPath(t *testing.T) {
	ideRoot := filepath.Join(filepath.FromSlash("/Users/me"), ".gemini", "antigravity-ide")
	transcript := filepath.Join(ideRoot, "brain", "447f1fc3-conv", ".system_generated", "logs", "transcript.jsonl")
	want := filepath.Join(ideRoot, "conversations", "447f1fc3-conv.db")
	if got := conversationDBPath(transcript); got != want {
		t.Fatalf("conversationDBPath = %q, want %q", got, want)
	}
}

func TestConversationDBPath_UnexpectedLayout(t *testing.T) {
	if got := conversationDBPath("/some/random/path/transcript.jsonl"); got != "" {
		t.Fatalf("conversationDBPath = %q, want empty for non-brain layout", got)
	}
}

func TestScanProtobufModelString(t *testing.T) {
	// Mirrors the real protobuf framing observed in Antigravity's
	// conversation db: a field tag, then a length byte, then exactly that
	// many bytes of model-name payload.
	blob := append([]byte{0x9a, 0x01, 0x10}, []byte("gemini-3-flash-a")...)
	blob = append(blob, []byte{0xa2, 0x01, 0x21}...)
	if got := scanProtobufModelString(blob); got != "gemini-3-flash-a" {
		t.Fatalf("scanProtobufModelString = %q, want gemini-3-flash-a", got)
	}
}

func TestScanProtobufModelString_IgnoresLengthMismatch(t *testing.T) {
	// "gemini-test" is a project name, not a model id — and even where it
	// appears verbatim, its byte length won't match a coincidental preceding
	// length byte that also satisfies the regex (which requires a digit after
	// "gemini-"), so it must never be returned.
	blob := []byte("...training/gemini-test/index.html...")
	if got := scanProtobufModelString(blob); got != "" {
		t.Fatalf("scanProtobufModelString = %q, want empty", got)
	}
}

func TestAntigravityGeminiWatcher_ResolvesModelFromConversationDB(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@l"}, {"config", "user.name", "T"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if err := cmd.Run(); err != nil {
			t.Skipf("git not available: %v", err)
		}
	}
	target := filepath.Join(repo, "style.css")
	if err := os.WriteFile(target, []byte("body { margin: 0; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir() // .../antigravity-ide
	convID := "9c2af0a1-conv"
	logDir := filepath.Join(root, "brain", convID, ".system_generated", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(logDir, "transcript.jsonl")
	step := map[string]any{
		"step_index": 3,
		"source":     "MODEL",
		"type":       "CODE_ACTION",
		"status":     "DONE",
		"created_at": "2026-06-08T01:00:00Z",
		"content":    "Created file file://" + target + " with requested content.",
	}
	line, err := json.Marshal(step)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, append(line, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	convDir := filepath.Join(root, "conversations")
	if err := os.MkdirAll(convDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(convDir, convID+".db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE gen_metadata (idx INTEGER, data BLOB)"); err != nil {
		t.Fatal(err)
	}
	// Mirror the real protobuf framing: field tag, length byte, then exactly
	// that many bytes of model-name payload.
	blob := append([]byte{0x9a, 0x01, 0x14}, []byte("gemini-3.5-flash-low")...)
	if _, err := db.Exec("INSERT INTO gen_metadata (idx, data) VALUES (?, ?)", 1, blob); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	sink := &captureSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	w := &antigravityTranscriptWatcher{roots: []string{filepath.Join(root, "brain")}}
	_ = w.run(ctx, sink)

	if len(sink.events) != 1 {
		t.Fatalf("events = %d, want 1: %+v", len(sink.events), sink.events)
	}
	if got := sink.events[0].Model; got != "gemini-3.5-flash-low" {
		t.Errorf("model = %q, want gemini-3.5-flash-low", got)
	}
}
