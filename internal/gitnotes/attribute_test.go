package gitnotes

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/blamely/blamely/internal/store"
)

// ---- coversLine ----

func TestCoversLine_Inside(t *testing.T) {
	lines := []store.EditLine{{StartLine: 5, EndLine: 10}}
	for _, n := range []int{5, 7, 10} {
		if !coversLine(lines, n) {
			t.Errorf("line %d should be covered by [5,10]", n)
		}
	}
}

func TestCoversLine_Outside(t *testing.T) {
	lines := []store.EditLine{{StartLine: 5, EndLine: 10}}
	for _, n := range []int{1, 4, 11, 100} {
		if coversLine(lines, n) {
			t.Errorf("line %d should NOT be covered by [5,10]", n)
		}
	}
}

func TestCoversLine_MultipleRanges(t *testing.T) {
	lines := []store.EditLine{
		{StartLine: 1, EndLine: 3},
		{StartLine: 8, EndLine: 10},
	}
	if !coversLine(lines, 2) {
		t.Error("line 2 should be covered by first range")
	}
	if !coversLine(lines, 9) {
		t.Error("line 9 should be covered by second range")
	}
	if coversLine(lines, 5) {
		t.Error("line 5 is in the gap, should NOT be covered")
	}
}

func TestCoversLine_Empty(t *testing.T) {
	if coversLine(nil, 1) {
		t.Error("empty lines should cover nothing")
	}
}

// ---- mergeEditsByTimeDesc ----

func makeEdit(ts int64) store.Edit {
	return store.Edit{TimestampNanos: ts}
}

func TestMergeEditsByTimeDesc_BothSorted(t *testing.T) {
	a := []store.Edit{makeEdit(100), makeEdit(60)}
	b := []store.Edit{makeEdit(80), makeEdit(40)}
	got := mergeEditsByTimeDesc(a, b)
	want := []int64{100, 80, 60, 40}
	if len(got) != len(want) {
		t.Fatalf("len: want %d, got %d", len(want), len(got))
	}
	for i, e := range got {
		if e.TimestampNanos != want[i] {
			t.Errorf("index %d: want %d, got %d", i, want[i], e.TimestampNanos)
		}
	}
}

func TestMergeEditsByTimeDesc_OneEmpty(t *testing.T) {
	a := []store.Edit{makeEdit(10), makeEdit(5)}
	got := mergeEditsByTimeDesc(a, nil)
	if len(got) != 2 || got[0].TimestampNanos != 10 {
		t.Errorf("expected unchanged a, got %v", got)
	}
}

func TestMergeEditsByTimeDesc_BothEmpty(t *testing.T) {
	got := mergeEditsByTimeDesc(nil, nil)
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestMergeEditsByTimeDesc_TieBreak(t *testing.T) {
	// Same timestamp — order between the two is stable (a before b).
	a := []store.Edit{makeEdit(50)}
	b := []store.Edit{makeEdit(50)}
	got := mergeEditsByTimeDesc(a, b)
	if len(got) != 2 {
		t.Fatalf("expected 2, got %d", len(got))
	}
}

// ---- defaultConf / confidenceFor ----

func TestDefaultConf(t *testing.T) {
	if defaultConf(store.ToolCopilot) != store.ConfidenceLow {
		t.Error("copilot should have low confidence")
	}
	for _, tool := range []store.Tool{store.ToolClaude, store.ToolCursor, store.ToolCodex, store.ToolHuman} {
		if defaultConf(tool) != store.ConfidenceHigh {
			t.Errorf("%s should have high confidence", tool)
		}
	}
}

func TestConfidenceFor_NilEdit(t *testing.T) {
	// When there's no edit, defaults apply.
	conf := confidenceFor(store.ToolClaude, nil)
	if conf != store.ConfidenceHigh {
		t.Errorf("want high, got %s", conf)
	}
	conf = confidenceFor(store.ToolCopilot, nil)
	if conf != store.ConfidenceLow {
		t.Errorf("want low for copilot, got %s", conf)
	}
}

func TestConfidenceFor_EditConfidence(t *testing.T) {
	e := &store.Edit{Confidence: store.ConfidenceMedium}
	if got := confidenceFor(store.ToolCursor, e); got != store.ConfidenceMedium {
		t.Errorf("want medium from edit, got %s", got)
	}
}

// ---- hasUsage / nullInt64 ----

func TestHasUsage(t *testing.T) {
	empty := &store.Edit{}
	if hasUsage(empty) {
		t.Error("empty edit should have no usage")
	}
	withInput := &store.Edit{InputTokens: sql.NullInt64{Valid: true, Int64: 100}}
	if !hasUsage(withInput) {
		t.Error("edit with input_tokens should have usage")
	}
}

func TestNullInt64(t *testing.T) {
	if nullInt64(sql.NullInt64{Valid: false}) != 0 {
		t.Error("invalid NullInt64 should return 0")
	}
	if nullInt64(sql.NullInt64{Valid: true, Int64: 42}) != 42 {
		t.Error("valid NullInt64 should return its value")
	}
}

// ---- buildNote: per-line shape, deletions, suggested vs accepted, zero by_tool ----

func openTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.OpenAt(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestBuildNote_PerLineShape_NoCollapse_GenType_Deletions(t *testing.T) {
	db := openTestDB(t)
	repo := "/r"
	commitNanos := time.Now().UnixNano()

	// Claude edit covering lines 1..3 of foo.go, with model + gen_type=chat.
	_, err := db.InsertEdit(store.Edit{
		TimestampNanos: commitNanos - int64(10*time.Second),
		RepoPath:       repo,
		FilePath:       "foo.go",
		Tool:           store.ToolClaude,
		Confidence:     store.ConfidenceHigh,
		GenType:        store.GenTypeChat,
		Model:          sql.NullString{Valid: true, String: "claude-opus"},
		SuggestedLines: 10, // AI proposed 10 lines; only 1..3 actually stuck
		Lines:          []store.EditLine{{StartLine: 1, EndLine: 3}},
	})
	if err != nil {
		t.Fatal(err)
	}

	added := []AddedLine{
		{File: "foo.go", LineNum: 1},
		{File: "foo.go", LineNum: 2},
		{File: "foo.go", LineNum: 3},
		// Lines 4-5 weren't covered by any edit → should land as human.
		{File: "foo.go", LineNum: 4},
		{File: "foo.go", LineNum: 5},
	}
	deleted := map[string][]int{
		"foo.go": {10, 11}, // two deleted lines in the pre-image
	}

	note, err := buildNote(db, repo, "deadbeef", commitNanos, added, deleted, nil, nil)
	if err != nil {
		t.Fatalf("buildNote: %v", err)
	}

	// by_tool should NOT contain any tool with zero lines, but SHOULD contain
	// claude (3 accepted lines, 10 suggested) and human (2 lines).
	for _, name := range []string{"cursor", "codex", "copilot"} {
		if _, ok := note.ByTool[name]; ok {
			t.Errorf("by_tool should not contain zero-line tool %q, got %#v", name, note.ByTool[name])
		}
	}
	c, ok := note.ByTool["claude"]
	if !ok {
		t.Fatal("expected claude in by_tool")
	}
	if c.Lines != 3 || c.AcceptedLines != 3 {
		t.Errorf("claude lines/accepted: want 3/3, got %d/%d", c.Lines, c.AcceptedLines)
	}
	if c.SuggestedLines != 10 {
		t.Errorf("claude suggested: want 10, got %d", c.SuggestedLines)
	}
	if c.Model == nil || *c.Model != "claude-opus" {
		t.Errorf("claude model: want claude-opus, got %v", c.Model)
	}

	if hum, ok := note.ByTool["human"]; !ok || hum.Lines != 2 {
		t.Errorf("expected human:2 in by_tool, got %v ok=%v", hum, ok)
	}

	// Files: one file, lines fully expanded (no Range field), deletions present.
	if len(note.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(note.Files))
	}
	f := note.Files[0]
	if f.Added != 5 {
		t.Errorf("added: want 5, got %d", f.Added)
	}
	if f.Deleted != 2 {
		t.Errorf("deleted: want 2, got %d", f.Deleted)
	}
	// Total entries: 5 adds + 2 deletes = 7.
	if len(f.Lines) != 7 {
		t.Fatalf("expected 7 LineEntry rows (5 adds + 2 deletes), got %d", len(f.Lines))
	}
	// Each entry must have Type set.
	for i, l := range f.Lines {
		if l.Type != "add" && l.Type != "delete" {
			t.Errorf("entry %d: unexpected Type %q", i, l.Type)
		}
	}
	// Adds should carry gen_type=chat for the claude ones.
	for _, l := range f.Lines {
		if l.Type == "add" && l.Tool == "claude" {
			if l.GenType == nil || *l.GenType != "chat" {
				t.Errorf("claude add line %d: gen_type should be 'chat', got %v", l.Line, l.GenType)
			}
		}
	}
	// Lines must be sorted by Line number.
	for i := 1; i < len(f.Lines); i++ {
		if f.Lines[i].Line < f.Lines[i-1].Line {
			t.Errorf("lines not sorted: %d before %d", f.Lines[i-1].Line, f.Lines[i].Line)
		}
	}
	// Deletes must not have a tool attributed.
	for _, l := range f.Lines {
		if l.Type == "delete" && l.Tool != "" {
			t.Errorf("delete line %d should have empty Tool, got %q", l.Line, l.Tool)
		}
	}

	// Totals
	if note.Totals.AILines != 3 {
		t.Errorf("ai_lines: want 3, got %d", note.Totals.AILines)
	}
	if note.Totals.HumanLines != 2 {
		t.Errorf("human_lines: want 2, got %d", note.Totals.HumanLines)
	}
	if note.Totals.DeletedLines != 2 {
		t.Errorf("deleted_lines: want 2, got %d", note.Totals.DeletedLines)
	}
}

func TestBuildNote_DeletionOnlyCommit(t *testing.T) {
	db := openTestDB(t)
	commitNanos := time.Now().UnixNano()

	// No added lines at all; only deletions on foo.go.
	added := []AddedLine{}
	deleted := map[string][]int{
		"foo.go": {2, 4},
	}

	note, err := buildNote(db, "/r", "deadbeef", commitNanos, added, deleted, nil, nil)
	if err != nil {
		t.Fatalf("buildNote: %v", err)
	}

	if len(note.Files) != 1 {
		t.Fatalf("expected 1 file even with no additions, got %d", len(note.Files))
	}
	f := note.Files[0]
	if f.Path != "foo.go" {
		t.Errorf("file: want foo.go, got %q", f.Path)
	}
	if f.Added != 0 {
		t.Errorf("added: want 0, got %d", f.Added)
	}
	if f.Deleted != 2 {
		t.Errorf("deleted: want 2, got %d", f.Deleted)
	}
	if len(f.Lines) != 2 {
		t.Fatalf("expected 2 LineEntry rows, got %d", len(f.Lines))
	}
	for _, l := range f.Lines {
		if l.Type != "delete" {
			t.Errorf("line %d: type should be 'delete', got %q", l.Line, l.Type)
		}
		if l.Tool != "" {
			t.Errorf("delete line %d should have empty Tool, got %q", l.Line, l.Tool)
		}
	}
	if note.Totals.DeletedLines != 2 {
		t.Errorf("deleted_lines total: want 2, got %d", note.Totals.DeletedLines)
	}
	if note.Totals.AILines != 0 || note.Totals.HumanLines != 0 {
		t.Errorf("ai/human totals: want 0/0 for deletion-only, got %d/%d",
			note.Totals.AILines, note.Totals.HumanLines)
	}
}
