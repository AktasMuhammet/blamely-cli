package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/blamely/blamely/internal/daemon"
	"github.com/blamely/blamely/internal/store"
)

// jsonQuotedFilePath returns a JSON string literal for a file URI (VS Code uses
// forward slashes). Raw Windows paths like C:\foo break JSON (\U… escapes).
func jsonQuotedFilePath(path string) string {
	b, err := json.Marshal(filepath.ToSlash(path))
	if err != nil {
		panic(err)
	}
	return string(b)
}

// captureSink records events emitted by a watcher for assertions.
type captureSink struct{ events []daemon.Event }

func (c *captureSink) Record(ev daemon.Event) error {
	c.events = append(c.events, ev)
	return nil
}

// newCopilotWatcher returns a chatSessionWatcher fixed to tool=copilot.
func newCopilotWatcher() *chatSessionWatcher { return &chatSessionWatcher{tool: store.ToolCopilot} }

// newCursorWatcher returns a chatSessionWatcher fixed to tool=cursor.
func newCursorWatcher() *chatSessionWatcher { return &chatSessionWatcher{tool: store.ToolCursor} }

// TestChatWatcher_ModelFromSnapshot verifies the kind=0 snapshot fix: the
// selected model is nested under inputState.selectedModel, and must be parsed
// from there (a prior bug read it at the top level, yielding model=?).
func TestChatWatcher_ModelFromSnapshot(t *testing.T) {
	w := newCopilotWatcher()
	st := &sessionState{}
	sink := &captureSink{}
	path := "/Users/x/Library/Application Support/Code/User/workspaceStorage/abc/chatSessions/s.jsonl"

	// Snapshot sets the model (nested).
	snap := `{"kind":0,"v":{"requests":[],"inputState":{"selectedModel":{"identifier":"copilot/gpt-5-mini"}}}}`
	w.handleLine(path, snap, st, sink, false, time.Now(), time.Now())
	if st.model != "gpt-5-mini" {
		t.Fatalf("snapshot model: want gpt-5-mini, got %q", st.model)
	}

	// A response append emits a chat marker carrying tool + model + path.
	resp := `{"kind":2,"k":["requests",0,"response"],"v":[{"value":"hi"}]}`
	w.handleLine(path, resp, st, sink, false, time.Now(), time.Now())
	if len(sink.events) != 1 {
		t.Fatalf("want 1 emitted event, got %d", len(sink.events))
	}
	ev := sink.events[0]
	if ev.Tool != "copilot" || ev.GenType != "chat" || ev.Model != "gpt-5-mini" {
		t.Errorf("event: tool=%q gen=%q model=%q, want copilot/chat/gpt-5-mini", ev.Tool, ev.GenType, ev.Model)
	}
	var meta struct {
		ChatSessionPath string `json:"chat_session_path"`
		Tool            string `json:"tool"`
	}
	if err := json.Unmarshal([]byte(ev.RawMeta), &meta); err != nil {
		t.Fatalf("raw_meta parse: %v (%s)", err, ev.RawMeta)
	}
	if meta.ChatSessionPath != path || meta.Tool != "copilot" {
		t.Errorf("raw_meta: path=%q tool=%q, want %q/copilot", meta.ChatSessionPath, meta.Tool, path)
	}
}

// TestChatWatcher_TextEditGroupEmitsChatEdit verifies the authoritative chat
// detector: a textEditGroup response part produces a per-file chat edit with the
// applied file, line range, tool, and model.
func TestChatWatcher_TextEditGroupEmitsChatEdit(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@l"}, {"config", "user.name", "T"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Skipf("git not available: %v", err)
		}
	}
	file := filepath.Join(dir, "app.go")
	if err := os.WriteFile(file, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	w := newCopilotWatcher()
	var mu sync.Mutex
	st := &sessionState{seenEdits: map[string]bool{}}
	st.model = "gpt-5-mini"
	sink := &captureSink{}

	// A finalized textEditGroup inserting 3 lines starting at line 2.
	var teg textEditGroupPart
	raw := fmt.Sprintf(`{"kind":"textEditGroup","uri":{"path":%s,"scheme":"file"},"edits":[[{"text":"a\nb\nc","range":{"startLineNumber":2,"startColumn":1,"endLineNumber":2,"endColumn":1}}],[]],"done":true}`,
		jsonQuotedFilePath(file))
	if err := json.Unmarshal([]byte(raw), &teg); err != nil {
		t.Fatal(err)
	}
	w.recordTextEditGroup(&teg, st.model, "/sessions/s.jsonl", st, &mu, sink, true, 0)

	if len(sink.events) != 1 {
		t.Fatalf("want 1 chat edit, got %d (%+v)", len(sink.events), sink.events)
	}
	ev := sink.events[0]
	if ev.Tool != "copilot" || ev.GenType != "chat" || ev.Confidence != "high" || ev.Model != "gpt-5-mini" {
		t.Errorf("event meta: tool=%q gen=%q conf=%q model=%q", ev.Tool, ev.GenType, ev.Confidence, ev.Model)
	}
	if ev.FilePath != "app.go" {
		t.Errorf("file: want app.go, got %q", ev.FilePath)
	}
	if len(ev.Lines) != 3 {
		t.Fatalf("lines: want 3 per-line entries, got %+v", ev.Lines)
	}
	for i, want := range []int{2, 3, 4} {
		if ev.Lines[i].Start != want || ev.Lines[i].End != want {
			t.Errorf("line %d: want %d-%d, got %d-%d", i, want, want, ev.Lines[i].Start, ev.Lines[i].End)
		}
		if ev.Lines[i].ContentSHA == "" {
			t.Errorf("line %d: missing content sha", i)
		}
	}

	// Re-recording the SAME edit (same request index) is deduped — no second event.
	w.recordTextEditGroup(&teg, st.model, "/sessions/s.jsonl", st, &mu, sink, true, 0)
	if len(sink.events) != 1 {
		t.Errorf("dedup failed: want still 1 event, got %d", len(sink.events))
	}

	// A non-done group must not emit.
	sink2 := &captureSink{}
	var partial textEditGroupPart
	_ = json.Unmarshal([]byte(fmt.Sprintf(`{"kind":"textEditGroup","uri":{"path":%s,"scheme":"file"},"edits":[[{"text":"x","range":{"startLineNumber":1}}]],"done":false}`,
		jsonQuotedFilePath(file))), &partial)
	w.recordTextEditGroup(&partial, st.model, "/sessions/s.jsonl", st, &mu, sink2, true, 0)
	if len(sink2.events) != 0 {
		t.Errorf("partial (done=false) should not emit, got %d", len(sink2.events))
	}
}

// TestChatWatcher_TextEditGroupSameStartLineDifferentRequest verifies that a
// follow-up chat turn which rewrites the same file starting at the same line
// (e.g. "regenerate the whole file") is NOT silently dropped just because an
// earlier turn already recorded an edit at that start line. This was the root
// cause of AI edits going undetected: the dedup key ignored which chat request
// (turn) the textEditGroup belonged to.
func TestChatWatcher_TextEditGroupSameStartLineDifferentRequest(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@l"}, {"config", "user.name", "T"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Skipf("git not available: %v", err)
		}
	}
	file := filepath.Join(dir, "app.go")
	if err := os.WriteFile(file, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	w := newCopilotWatcher()
	var mu sync.Mutex
	st := &sessionState{seenEdits: map[string]bool{}}
	st.model = "gpt-5-mini"
	sink := &captureSink{}

	// Turn 0: a textEditGroup rewriting the file from line 1 (e.g. matches the
	// pre-edit/HEAD content).
	var teg0 textEditGroupPart
	raw0 := fmt.Sprintf(`{"kind":"textEditGroup","uri":{"path":%s,"scheme":"file"},"edits":[[{"text":"old\nfile\ncontent","range":{"startLineNumber":1}}]],"done":true}`,
		jsonQuotedFilePath(file))
	if err := json.Unmarshal([]byte(raw0), &teg0); err != nil {
		t.Fatal(err)
	}
	w.recordTextEditGroup(&teg0, st.model, "/sessions/s.jsonl", st, &mu, sink, true, 0)
	if len(sink.events) != 1 {
		t.Fatalf("turn 0: want 1 event, got %d", len(sink.events))
	}

	// Turn 1: a follow-up textEditGroup ALSO starting at line 1 (the model
	// regenerated the whole file again with new content). Must still emit.
	var teg1 textEditGroupPart
	raw1 := fmt.Sprintf(`{"kind":"textEditGroup","uri":{"path":%s,"scheme":"file"},"edits":[[{"text":"new\nfile\ncontent\nwith\nmore\nlines","range":{"startLineNumber":1}}]],"done":true}`,
		jsonQuotedFilePath(file))
	if err := json.Unmarshal([]byte(raw1), &teg1); err != nil {
		t.Fatal(err)
	}
	w.recordTextEditGroup(&teg1, st.model, "/sessions/s.jsonl", st, &mu, sink, true, 1)
	if len(sink.events) != 2 {
		t.Fatalf("turn 1: want 2 events total, got %d (%+v)", len(sink.events), sink.events)
	}
	if len(sink.events[1].Lines) != 6 {
		t.Errorf("turn 1: want 6 line entries, got %d", len(sink.events[1].Lines))
	}
}

// TestScanTextEdits_FullFile verifies the full-file scanner: it emits a chat
// edit for a textEditGroup (even from a fresh file), no-ops when the mtime is
// unchanged, and emits only the NEW edit (deduping the old) after an append.
func TestScanTextEdits_FullFile(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@l"}, {"config", "user.name", "T"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if err := cmd.Run(); err != nil {
			t.Skipf("git not available: %v", err)
		}
	}
	target := filepath.Join(repo, "b.go")
	if err := os.WriteFile(target, []byte("package b\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sessDir := t.TempDir()
	sess := filepath.Join(sessDir, "s.jsonl")
	line1 := `{"kind":0,"v":{"requests":[],"inputState":{"selectedModel":{"identifier":"copilot/gpt-4o"}}}}` + "\n"
	teg := fmt.Sprintf(`{"kind":2,"k":["requests",0,"response"],"v":[{"kind":"textEditGroup","uri":{"path":%s,"scheme":"file"},"edits":[[{"text":"x\ny","range":{"startLineNumber":2}}]],"done":true}]}`,
		jsonQuotedFilePath(target)) + "\n"
	if err := os.WriteFile(sess, []byte(line1+teg), 0o644); err != nil {
		t.Fatal(err)
	}

	w := newCopilotWatcher()
	var mu sync.Mutex
	st := &sessionState{}
	sink := &captureSink{}

	// Fresh file (mtime now) → emit even on first scan.
	w.scanTextEdits(sess, st, &mu, sink, time.Now())
	if len(sink.events) != 1 {
		t.Fatalf("first scan: want 1 event, got %d", len(sink.events))
	}
	if sink.events[0].Tool != "copilot" || sink.events[0].GenType != "chat" || sink.events[0].FilePath != "b.go" {
		t.Errorf("unexpected event: %+v", sink.events[0])
	}

	// Same mtime → no re-scan, no duplicate.
	w.scanTextEdits(sess, st, &mu, sink, st.tegMtime)
	if len(sink.events) != 1 {
		t.Errorf("unchanged mtime: want 1 event, got %d", len(sink.events))
	}

	// Append a NEW edit and advance mtime → only the new one emits.
	teg2 := fmt.Sprintf(`{"kind":2,"k":["requests",1,"response"],"v":[{"kind":"textEditGroup","uri":{"path":%s,"scheme":"file"},"edits":[[{"text":"z","range":{"startLineNumber":9}}]],"done":true}]}`,
		jsonQuotedFilePath(target)) + "\n"
	fh, _ := os.OpenFile(sess, os.O_APPEND|os.O_WRONLY, 0o644)
	fh.WriteString(teg2)
	fh.Close()
	w.scanTextEdits(sess, st, &mu, sink, time.Now().Add(time.Second))
	if len(sink.events) != 2 {
		t.Fatalf("after append: want 2 events total, got %d", len(sink.events))
	}
	if sink.events[1].Lines[0].Start != 9 {
		t.Errorf("new edit line: want 9, got %d", sink.events[1].Lines[0].Start)
	}
}

// TestFindTextEditGroups_Nested verifies textEditGroups are found inside a
// kind=0 snapshot tree, not just a flat kind=2 response array.
func TestFindTextEditGroups_Nested(t *testing.T) {
	snapshot := json.RawMessage(`{"requests":[{"response":[{"kind":"thinking","value":"x"},{"kind":"textEditGroup","uri":{"path":"/a/b.go","scheme":"file"},"edits":[[{"text":"x\ny","range":{"startLineNumber":5}}]],"done":true}]}]}`)
	var out []textEditGroupPart
	findTextEditGroups(snapshot, &out)
	if len(out) != 1 {
		t.Fatalf("want 1 textEditGroup found in snapshot, got %d", len(out))
	}
	if out[0].URI.Path != "/a/b.go" || !out[0].Done {
		t.Errorf("unexpected: %+v", out[0])
	}
}

// TestCursorChatWatcher_AlwaysEmitsCursor verifies that CursorChatWatcher
// always emits tool=cursor regardless of which model is selected. This is the
// key separation guarantee: Cursor sessions handled by CursorChatWatcher are
// never misclassified as Copilot.
func TestCursorChatWatcher_AlwaysEmitsCursor(t *testing.T) {
	w := newCursorWatcher()
	st := &sessionState{}
	sink := &captureSink{}
	path := "/x/Cursor/User/workspaceStorage/abc/chatSessions/s.jsonl"

	// Even a model with a copilot/ prefix emits tool=cursor when handled by
	// the cursor watcher — the watcher's fixed tool overrides model classification.
	snap := `{"kind":0,"v":{"requests":[],"inputState":{"selectedModel":{"identifier":"claude-3.5-sonnet"}}}}`
	w.handleLine(path, snap, st, sink, false, time.Now(), time.Now())
	w.handleLine(path, `{"kind":2,"k":["requests",0,"response"],"v":[{"value":"x"}]}`, st, sink, false, time.Now(), time.Now())
	if len(sink.events) != 1 || sink.events[0].Tool != "cursor" {
		t.Fatalf("want a single cursor event, got %+v", sink.events)
	}
}

// TestCopilotChatWatcher_AlwaysEmitsCopilot is the mirror: CopilotChatWatcher
// always emits tool=copilot regardless of model.
func TestCopilotChatWatcher_AlwaysEmitsCopilot(t *testing.T) {
	w := newCopilotWatcher()
	st := &sessionState{}
	sink := &captureSink{}
	path := "/x/Code/User/workspaceStorage/abc/chatSessions/s.jsonl"

	snap := `{"kind":0,"v":{"requests":[],"inputState":{"selectedModel":{"identifier":"copilot/gpt-5-mini"}}}}`
	w.handleLine(path, snap, st, sink, false, time.Now(), time.Now())
	w.handleLine(path, `{"kind":2,"k":["requests",0,"response"],"v":[{"value":"x"}]}`, st, sink, false, time.Now(), time.Now())
	if len(sink.events) != 1 || sink.events[0].Tool != "copilot" {
		t.Fatalf("want a single copilot event, got %+v", sink.events)
	}
}
