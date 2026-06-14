package gitnotes

import (
	"testing"
	"time"

	"github.com/blamely/blamely/internal/store"
)

// Regression for the Codex report: when the AI generates repeated structural
// lines (closing braces) and blank-line gaps, and the file drifts across
// incremental applies so those lines no longer sit at their recorded positions,
// they must stay AI — not flip to Human as copy-pastes. Blank lines inherit the
// AI block around them.
func TestBuildNote_AIDuplicateAndBlankLinesStayAI(t *testing.T) {
	db := openTestDB(t)
	commitNanos := time.Now().UnixNano()
	mk := func(n int, s string) store.EditLine {
		return store.EditLine{StartLine: n, EndLine: n, ContentSHA: sha256HexStr([]byte(s))}
	}
	// AI recorded three CSS blocks; "}" repeats three times.
	if _, err := db.InsertEdit(store.Edit{
		TimestampNanos: commitNanos - int64(5*time.Second),
		RepoPath:       "/r",
		FilePath:       "a.css",
		Tool:           store.ToolCodex,
		Confidence:     store.ConfidenceHigh,
		GenType:        store.GenTypeCLI,
		Lines:          []store.EditLine{mk(1, "a {"), mk(2, "}"), mk(4, "b {"), mk(5, "}"), mk(7, "c {"), mk(8, "}")},
	}); err != nil {
		t.Fatal(err)
	}
	// Committed file: a human header line at top shifts everything down by one,
	// so every AI line (including the three "}") is now drifted off its recorded
	// position. Blank lines sit between the blocks.
	added := []AddedLine{
		{File: "a.css", LineNum: 1, Content: "header"},
		{File: "a.css", LineNum: 2, Content: "a {"},
		{File: "a.css", LineNum: 3, Content: "}"},
		{File: "a.css", LineNum: 4, Content: ""},
		{File: "a.css", LineNum: 5, Content: "b {"},
		{File: "a.css", LineNum: 6, Content: "}"},
		{File: "a.css", LineNum: 7, Content: ""},
		{File: "a.css", LineNum: 8, Content: "c {"},
		{File: "a.css", LineNum: 9, Content: "}"},
	}
	note, err := buildNote(db, "/r", "sha", commitNanos, added, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int]string{}
	for _, l := range expandLines(note.Files[0]) {
		if l.Type == "add" {
			got[l.Line] = l.AuthorType
		}
	}
	want := map[int]string{1: "Human", 2: "AI", 3: "AI", 4: "AI", 5: "AI", 6: "AI", 7: "AI", 8: "AI", 9: "AI"}
	for ln := 1; ln <= 9; ln++ {
		if got[ln] != want[ln] {
			t.Errorf("line %d (%q): want %s, got %s", ln, added[ln-1].Content, want[ln], got[ln])
		}
	}
}
