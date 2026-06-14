package gitnotes

import (
	"testing"
	"time"

	"github.com/blamely/blamely/internal/store"
)

// Regression for the reported bug: the AI writes 6 lines; the user then inserts
// 2 lines above the lower half, shifting AI lines l3..l6 down to lines 5..8.
// They must stay AI in the committed note (the gutter already showed them
// correctly). The tricky part is the SECOND variant, where the editor's
// human-edit watcher diffed the file after the insert and recorded a NEWER
// human row carrying those shifted AI lines' content_sha at their NEW
// positions. Without the AI-authorship preference that newer human row stole
// the lines and flipped them AI -> Human.
//
//	before (AI):      after user inserts h1,h2 at 3..4:
//	  1 l1               1 l1   (AI)
//	  2 l2               2 l2   (AI)
//	  3 l3               3 h1   (Human)
//	  4 l4               4 h2   (Human)
//	  5 l5               5 l3   (AI, shifted)
//	  6 l6               6 l4   (AI, shifted)
//	                     7 l5   (AI, shifted)
//	                     8 l6   (AI, shifted)

func aiSixLineEdit(commitNanos int64) store.Edit {
	mk := func(n int, s string) store.EditLine {
		return store.EditLine{StartLine: n, EndLine: n, ContentSHA: sha256HexStr([]byte(s))}
	}
	return store.Edit{
		TimestampNanos: commitNanos - int64(10*time.Second),
		RepoPath:       "/r",
		FilePath:       "app.go",
		Tool:           store.ToolClaude,
		Confidence:     store.ConfidenceHigh,
		GenType:        store.GenTypeChat,
		Lines:          []store.EditLine{mk(1, "l1"), mk(2, "l2"), mk(3, "l3"), mk(4, "l4"), mk(5, "l5"), mk(6, "l6")},
	}
}

func shiftedAddedLines() []AddedLine {
	return []AddedLine{
		{File: "app.go", LineNum: 1, Content: "l1"},
		{File: "app.go", LineNum: 2, Content: "l2"},
		{File: "app.go", LineNum: 3, Content: "h1"},
		{File: "app.go", LineNum: 4, Content: "h2"},
		{File: "app.go", LineNum: 5, Content: "l3"},
		{File: "app.go", LineNum: 6, Content: "l4"},
		{File: "app.go", LineNum: 7, Content: "l5"},
		{File: "app.go", LineNum: 8, Content: "l6"},
	}
}

func assertShiftAttribution(t *testing.T, db *store.DB, commitNanos int64) {
	t.Helper()
	note, err := buildNote(db, "/r", "abc123", commitNanos, shiftedAddedLines(), nil, nil, nil)
	if err != nil {
		t.Fatalf("buildNote: %v", err)
	}
	got := map[int]string{}
	for _, l := range expandLines(note.Files[0]) {
		if l.Type == "add" {
			got[l.Line] = l.AuthorType
		}
	}
	want := map[int]string{1: "AI", 2: "AI", 3: "Human", 4: "Human", 5: "AI", 6: "AI", 7: "AI", 8: "AI"}
	for ln := 1; ln <= 8; ln++ {
		if got[ln] != want[ln] {
			t.Errorf("line %d: AuthorType want %s, got %s", ln, want[ln], got[ln])
		}
	}
	if note.Totals.AILines != 6 || note.Totals.HumanLines != 2 {
		t.Errorf("totals: want AI=6 Human=2, got AI=%d Human=%d", note.Totals.AILines, note.Totals.HumanLines)
	}
}

// Pure content_sha drift: only the AI edit exists.
func TestBuildNote_HumanInsertShiftsAIBlock(t *testing.T) {
	db := openTestDB(t)
	commitNanos := time.Now().UnixNano()
	if _, err := db.InsertEdit(aiSixLineEdit(commitNanos)); err != nil {
		t.Fatal(err)
	}
	assertShiftAttribution(t, db, commitNanos)
}

// The real-world case: a NEWER human-edit row over-captured the shifted AI lines
// (content_sha at their new positions). The AI author must still win.
func TestBuildNote_HumanInsertShift_CompetingHumanRow(t *testing.T) {
	db := openTestDB(t)
	commitNanos := time.Now().UnixNano()
	if _, err := db.InsertEdit(aiSixLineEdit(commitNanos)); err != nil {
		t.Fatal(err)
	}
	mk := func(n int, s string) store.EditLine {
		return store.EditLine{StartLine: n, EndLine: n, ContentSHA: sha256HexStr([]byte(s))}
	}
	human := store.Edit{
		TimestampNanos: commitNanos - int64(2*time.Second), // newer than the AI edit
		RepoPath:       "/r",
		FilePath:       "app.go",
		Tool:           "",
		Confidence:     store.ConfidenceHigh,
		GenType:        store.GenTypeHuman,
		Lines:          []store.EditLine{mk(3, "h1"), mk(4, "h2"), mk(5, "l3"), mk(6, "l4"), mk(7, "l5"), mk(8, "l6")},
	}
	if _, err := db.InsertEdit(human); err != nil {
		t.Fatal(err)
	}
	assertShiftAttribution(t, db, commitNanos)
}
