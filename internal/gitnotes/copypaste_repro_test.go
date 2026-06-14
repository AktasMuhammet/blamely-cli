package gitnotes

import (
	"database/sql"
	"testing"
	"time"

	"github.com/blamely/blamely/internal/store"
)

// TestBuildNote_ReindentedPasteIsHuman reproduces the user's case: the AI wrote
// a line once (at line 42); the human then pasted two RE-INDENTED copies of it
// at lines 43 and 44. Their exact bytes differ (indentation) but they normalize
// to the same text. Expected: line 42 = AI (exact match), lines 43/44 = Human
// (the AI only produced the line once; re-indented copies beyond that are the
// human's paste).
func TestBuildNote_ReindentedPasteIsHuman(t *testing.T) {
	db := openTestDB(t)
	repo := "/r"
	now := time.Now().UnixNano()

	sp6 := "      <p>hint</p>"
	sp12 := "            <p>hint</p>"
	sp18 := "                  <p>hint</p>"

	// AI (current session — NULL session id) recorded the line ONCE at line 42.
	_, err := db.InsertEdit(store.Edit{
		TimestampNanos: now - int64(time.Minute),
		RepoPath:       repo,
		FilePath:       "register.html",
		Tool:           store.ToolClaude,
		Confidence:     store.ConfidenceHigh,
		GenType:        store.GenTypeChat,
		Lines: []store.EditLine{
			{StartLine: 42, EndLine: 42,
				ContentSHA:     sha256HexStr([]byte(sp6)),
				ContentSHANorm: sha256HexNormStr(sp6)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	added := []AddedLine{
		{File: "register.html", LineNum: 42, Content: sp6},
		{File: "register.html", LineNum: 43, Content: sp12},
		{File: "register.html", LineNum: 44, Content: sp18},
	}
	note, err := buildNote(db, repo, "sha1", now, added, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildNote: %v", err)
	}

	// Flatten author_type per committed line.
	got := map[int]string{}
	for _, f := range note.Files {
		for _, r := range f.Lines {
			for ln := r.Start; ln <= r.End; ln++ {
				got[ln] = r.AuthorType
			}
		}
	}
	t.Logf("line attribution: 42=%s 43=%s 44=%s  (ai=%d human=%d)",
		got[42], got[43], got[44], note.Totals.AILines, note.Totals.HumanLines)

	if got[42] != "AI" {
		t.Errorf("line 42 (original AI line): want AI, got %s", got[42])
	}
	if got[43] != "Human" {
		t.Errorf("line 43 (re-indented paste): want Human, got %s", got[43])
	}
	if got[44] != "Human" {
		t.Errorf("line 44 (re-indented paste): want Human, got %s", got[44])
	}
}

// TestBuildNote_ExactDuplicateBudgetNotDoubled reproduces the Antigravity bug:
// the AI wrote a line ONCE (recorded at a drifted position, so it matches by
// content not position); the human then made an EXACT duplicate of it. The
// exact-sha and normalized drift budgets must be shared, so only ONE of the two
// committed copies attributes to AI — the other (the human's duplicate) Human.
func TestBuildNote_ExactDuplicateBudgetNotDoubled(t *testing.T) {
	db := openTestDB(t)
	repo := "/r"
	now := time.Now().UnixNano()

	line := "    .card {" // a distinctive line the AI wrote exactly once
	_, err := db.InsertEdit(store.Edit{
		TimestampNanos: now - int64(time.Minute),
		RepoPath:       repo,
		FilePath:       "login.html",
		Tool:           store.ToolGemini,
		Confidence:     store.ConfidenceHigh,
		GenType:        store.GenTypeChat,
		Lines: []store.EditLine{
			// recorded at line 76 — neither committed copy is at this position,
			// so both can only match by content (drift), exercising the budgets.
			{StartLine: 76, EndLine: 76,
				ContentSHA:     sha256HexStr([]byte(line)),
				ContentSHANorm: sha256HexNormStr(line)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	added := []AddedLine{
		{File: "login.html", LineNum: 84, Content: line},  // AI's original (drifted)
		{File: "login.html", LineNum: 102, Content: line}, // human's exact duplicate
	}
	note, err := buildNote(db, repo, "sha1", now, added, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildNote: %v", err)
	}
	if note.Totals.AILines != 1 || note.Totals.HumanLines != 1 {
		t.Errorf("want AI 1 / Human 1 (AI wrote it once), got AI %d / Human %d",
			note.Totals.AILines, note.Totals.HumanLines)
	}
}

// TestBuildNote_MultiRecordingDoesNotInflateBudget reproduces the Antigravity
// swap/jagged bug: the AI re-recorded the same file across TWO edits (each
// recording a line ONCE). Summing them would grant budget 2 and let a human's
// single exact duplicate read as AI. With max-over-edits the budget is 1, so
// the original is AI and the paste is Human.
func TestBuildNote_MultiRecordingDoesNotInflateBudget(t *testing.T) {
	db := openTestDB(t)
	repo := "/r"
	now := time.Now().UnixNano()

	line := "  <div class=\"feature\">"
	mk := func(ts int64, at int) store.Edit {
		return store.Edit{
			TimestampNanos: ts, RepoPath: repo, FilePath: "index.html",
			Tool: store.ToolGemini, Confidence: store.ConfidenceHigh, GenType: store.GenTypeChat,
			Lines: []store.EditLine{{StartLine: at, EndLine: at,
				ContentSHA: sha256HexStr([]byte(line)), ContentSHANorm: sha256HexNormStr(line)}},
		}
	}
	// Two AI edits (re-recordings) each with the line once, at drifted positions.
	if _, err := db.InsertEdit(mk(now-int64(2*time.Minute), 46)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertEdit(mk(now-int64(time.Minute), 50)); err != nil {
		t.Fatal(err)
	}

	// Commit has the AI line at 46 (original) and an exact copy the human pasted at 59.
	added := []AddedLine{
		{File: "index.html", LineNum: 46, Content: line},
		{File: "index.html", LineNum: 59, Content: line},
	}
	note, err := buildNote(db, repo, "sha1", now, added, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildNote: %v", err)
	}
	if note.Totals.AILines != 1 || note.Totals.HumanLines != 1 {
		t.Errorf("want AI 1 / Human 1 (AI authored one copy across re-recordings), got AI %d / Human %d",
			note.Totals.AILines, note.Totals.HumanLines)
	}
}

// TestBuildNote_DeletionOnlyPopulatesByTool reproduces the empty-by_tool bug:
// an AI tool deleted a whole file (no additions). by_tool was built only from
// added lines, leaving it empty; now the tool appears with DeletedLines set.
func TestBuildNote_DeletionOnlyPopulatesByTool(t *testing.T) {
	db := openTestDB(t)
	repo := "/r"
	now := time.Now().UnixNano()

	const lineA, lineB = "  <h1>title</h1>", "  <p>body</p>"
	// AI edit that RECORDED removing these two lines (edit_removed_lines).
	_, err := db.InsertEdit(store.Edit{
		TimestampNanos: now - int64(time.Minute),
		RepoPath:       repo, FilePath: "page.html",
		Tool: store.ToolGemini, Confidence: store.ConfidenceHigh, GenType: store.GenTypeChat,
		Model:        sqlNullString("gemini-3-flash"),
		RemovedLines: []store.RemovedLineHash{{ContentSHA: sha256HexStr([]byte(lineA))}, {ContentSHA: sha256HexStr([]byte(lineB))}},
	})
	if err != nil {
		t.Fatal(err)
	}
	deleted := map[string][]DeletedLine{"page.html": {{LineNum: 1, Content: lineA}, {LineNum: 2, Content: lineB}}}
	fileChanges := map[string]FileChangeType{"page.html": FileDeleted}

	note, err := buildNote(db, repo, "sha1", now, nil, deleted, nil, fileChanges)
	if err != nil {
		t.Fatalf("buildNote: %v", err)
	}
	if note.Totals.AIDeletedLines != 2 {
		t.Fatalf("ai_deleted_lines = %d, want 2", note.Totals.AIDeletedLines)
	}
	tl, ok := note.ByTool["gemini"]
	if !ok {
		t.Fatalf("by_tool must contain gemini for a gemini deletion, got %+v", note.ByTool)
	}
	if tl.DeletedLines != 2 {
		t.Errorf("gemini DeletedLines = %d, want 2", tl.DeletedLines)
	}
	if tl.Lines != 0 {
		t.Errorf("gemini Lines = %d, want 0 (deletion-only)", tl.Lines)
	}
	if tl.Model == nil || *tl.Model != "gemini-3-flash" {
		t.Errorf("gemini model should be backfilled from the delete range, got %v", tl.Model)
	}
}

func sqlNullString(s string) sql.NullString { return sql.NullString{Valid: true, String: s} }

// TestBuildNote_PastedBlockIsCoherentlyHuman: the AI wrote a multi-line block
// once; the human pasted an exact copy of it. Per-line matching splits the copy
// jaggedly; coalesceDuplicateBlocks makes the whole pasted block Human and keeps
// the original AI.
func TestBuildNote_PastedBlockIsCoherentlyHuman(t *testing.T) {
	db := openTestDB(t)
	repo := "/r"
	now := time.Now().UnixNano()

	// A 4-line block the AI wrote ONCE (recorded at lines 35-38).
	block := []string{"  body {", "    margin: 0;", "    display: flex;", "  }"}
	var edLines []store.EditLine
	for i, b := range block {
		edLines = append(edLines, store.EditLine{StartLine: 35 + i, EndLine: 35 + i,
			ContentSHA: sha256HexStr([]byte(b)), ContentSHANorm: sha256HexNormStr(b)})
	}
	if _, err := db.InsertEdit(store.Edit{
		TimestampNanos: now - int64(time.Minute), RepoPath: repo, FilePath: "page.html",
		Tool: store.ToolGemini, Confidence: store.ConfidenceHigh, GenType: store.GenTypeChat,
		Model: sqlNullString("gemini-3"), Lines: edLines,
	}); err != nil {
		t.Fatal(err)
	}

	// Commit: the original block at 35-38 AND a human-pasted copy at 48-51.
	var added []AddedLine
	for i, b := range block {
		added = append(added, AddedLine{File: "page.html", LineNum: 35 + i, Content: b})
	}
	for i, b := range block {
		added = append(added, AddedLine{File: "page.html", LineNum: 48 + i, Content: b})
	}
	note, err := buildNote(db, repo, "sha1", now, added, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Flatten author_type per line.
	at := map[int]string{}
	for _, f := range note.Files {
		for _, r := range f.Lines {
			for ln := r.Start; ln <= r.End; ln++ {
				at[ln] = r.AuthorType
			}
		}
	}
	for ln := 35; ln <= 38; ln++ {
		if at[ln] != "AI" {
			t.Errorf("original block L%d: want AI, got %s", ln, at[ln])
		}
	}
	for ln := 48; ln <= 51; ln++ {
		if at[ln] != "Human" {
			t.Errorf("pasted block L%d: want Human, got %s", ln, at[ln])
		}
	}
}
