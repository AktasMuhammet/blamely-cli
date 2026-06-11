package gitnotes

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/blamely/blamely/internal/store"
)

// expandLines flattens collapsed RangeEntry ranges back into one LineEntry per
// line so per-line test assertions stay meaningful after range collapsing.
func expandLines(f FileEntry) []LineEntry {
	var out []LineEntry
	for _, r := range f.Lines {
		for ln := r.Start; ln <= r.End; ln++ {
			out = append(out, LineEntry{
				Line:       ln,
				Type:       r.Type,
				AuthorType: r.AuthorType,
				Tool:       r.Tool,
				Model:      r.Model,
				GenType:    r.GenType,
			})
		}
	}
	return out
}

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

func TestCollapseToRanges(t *testing.T) {
	chat := "chat"
	human := "human"
	mk := func(line int, typ, author, tool string, gt *string) LineEntry {
		return LineEntry{Line: line, Type: typ, AuthorType: author, Tool: tool, GenType: gt}
	}
	in := []LineEntry{
		// Contiguous AI/claude/chat 1..3 → one range.
		mk(1, "add", "AI", "claude", &chat),
		mk(2, "add", "AI", "claude", &chat),
		mk(3, "add", "AI", "claude", &chat),
		// Human 4..5 → separate range (attribution changes).
		mk(4, "add", "Human", "", &human),
		mk(5, "add", "Human", "", &human),
		// Gap at 6 (no entry); AI again at 7 → new range despite same attribution.
		mk(7, "add", "AI", "claude", &chat),
		// Delete at line 3 must NOT merge with the add range at 3 (Type differs).
		mk(3, "delete", "Human", "", nil),
	}
	got := collapseToRanges(in)
	want := []RangeEntry{
		{Start: 1, End: 3, Type: "add", AuthorType: "AI", Tool: "claude", GenType: &chat},
		{Start: 3, End: 3, Type: "delete", AuthorType: "Human"},
		{Start: 4, End: 5, Type: "add", AuthorType: "Human", GenType: &human},
		{Start: 7, End: 7, Type: "add", AuthorType: "AI", Tool: "claude", GenType: &chat},
	}
	if len(got) != len(want) {
		t.Fatalf("range count: want %d, got %d (%+v)", len(want), len(got), got)
	}
	for i := range want {
		g := got[i]
		w := want[i]
		if g.Start != w.Start || g.End != w.End || g.Type != w.Type ||
			g.AuthorType != w.AuthorType || g.Tool != w.Tool || !equalStrPtr(g.GenType, w.GenType) {
			t.Errorf("range %d: want %+v, got %+v", i, w, g)
		}
	}
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
	deleted := map[string][]DeletedLine{
		"foo.go": {{LineNum: 10, Content: "old line 10"}, {LineNum: 11, Content: "old line 11"}}, // two deleted lines in the pre-image, not recorded by any edit
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

	// Humans are NOT a tool: no entry in by_tool, the count surfaces under
	// totals.human_lines and by_gen_type.human instead.
	if _, ok := note.ByTool["human"]; ok {
		t.Errorf("by_tool must not contain 'human' — humans aren't a tool")
	}
	// 2 human-typed added lines (4,5) + 2 deleted lines (10,11) = 4.
	// Deleted lines are always attributed to the human and folded into
	// by_gen_type.human after the per-line totals are finalised.
	if note.ByGenType.Human != 4 {
		t.Errorf("by_gen_type.human: want 4, got %d", note.ByGenType.Human)
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
	// Total lines (expanded from ranges): 5 adds + 2 deletes = 7.
	lines := expandLines(f)
	if len(lines) != 7 {
		t.Fatalf("expected 7 lines (5 adds + 2 deletes), got %d", len(lines))
	}
	// Each entry must have Type set.
	for i, l := range lines {
		if l.Type != "add" && l.Type != "delete" {
			t.Errorf("entry %d: unexpected Type %q", i, l.Type)
		}
	}
	// Adds should carry gen_type=chat for the claude ones.
	for _, l := range lines {
		if l.Type == "add" && l.Tool == "claude" {
			if l.GenType == nil || *l.GenType != "chat" {
				t.Errorf("claude add line %d: gen_type should be 'chat', got %v", l.Line, l.GenType)
			}
		}
	}
	// Ranges must be emitted sorted by line number.
	for i := 1; i < len(lines); i++ {
		if lines[i].Line < lines[i-1].Line {
			t.Errorf("lines not sorted: %d before %d", lines[i-1].Line, lines[i].Line)
		}
	}
	// Deletes must not have a tool attributed.
	for _, l := range lines {
		if l.Type == "delete" && l.Tool != "" {
			t.Errorf("delete line %d should have empty Tool, got %q", l.Line, l.Tool)
		}
	}
	// Human-typed adds (lines 4 and 5 in this fixture) must have an
	// empty Tool (humans aren't a tool) and gen_type=human.
	humanLinesSeen := 0
	for _, l := range lines {
		if l.Type != "add" || l.AuthorType != "Human" {
			continue
		}
		humanLinesSeen++
		if l.Tool != "" {
			t.Errorf("human add line %d: Tool should be empty, got %q", l.Line, l.Tool)
		}
		if l.GenType == nil || *l.GenType != "human" {
			t.Errorf("human add line %d: gen_type should be 'human', got %v", l.Line, l.GenType)
		}
	}
	if humanLinesSeen != 2 {
		t.Errorf("expected 2 human add lines (4 and 5), saw %d", humanLinesSeen)
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

// TestBuildNote_SecondCommit_HumanOverridesAI is the core regression test for
// the "AI generates code → commit1 → human edits → commit2 shows human"
// scenario. With the new commit-time attribution model, commit2's sinceNanos
// is set to commit1's timestamp, so the AI edit recorded before commit1 is
// excluded from the lookup. The line therefore falls to the human default.
func TestBuildNote_SecondCommit_HumanOverridesAI(t *testing.T) {
	db := openTestDB(t)
	repo := "/r"
	now := time.Now().UnixNano()
	commit1Nanos := now - int64(60*time.Second) // commit1 happened 60s ago
	commit2Nanos := now                          // current commit

	// AI (claude) wrote lines 1-5 during session1, recorded before commit1.
	_, err := db.InsertEdit(store.Edit{
		TimestampNanos: commit1Nanos - int64(30*time.Second), // 90s before commit2
		RepoPath:       repo,
		FilePath:       "foo.go",
		Tool:           store.ToolClaude,
		Confidence:     store.ConfidenceHigh,
		GenType:        store.GenTypeChat,
		Lines:          []store.EditLine{{StartLine: 1, EndLine: 5}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// commit1 is recorded in the DB (MarkCommitNoted). This becomes prevCommitTS
	// for commit2's buildNote call, bounding the AI record lookup.
	if err := db.MarkCommitNoted("commit1sha", repo, commit1Nanos); err != nil {
		t.Fatal(err)
	}

	// Commit2 diff: the human modified line 3 (no AI record exists since commit1).
	added := []AddedLine{{File: "foo.go", LineNum: 3}}

	note, err := buildNote(db, repo, "deadbeef", commit2Nanos, added, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildNote: %v", err)
	}

	if len(note.Files) != 1 {
		t.Fatalf("expected 1 file, got files=%v", note.Files)
	}
	lines := expandLines(note.Files[0])
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	l := lines[0]

	// AI edit at T-90s is before prevCommitTS=commit1Nanos → excluded.
	// Line 3 has no AI record in the current session → defaults to Human.
	if l.AuthorType != "Human" {
		t.Errorf("line 3: AuthorType want Human, got %q", l.AuthorType)
	}
	if l.Tool != "" {
		t.Errorf("line 3: Tool want '' (human, not a tool), got %q", l.Tool)
	}
	if l.GenType == nil || *l.GenType != "human" {
		t.Errorf("line 3: GenType want 'human', got %v", l.GenType)
	}

	if note.Totals.HumanLines != 1 {
		t.Errorf("human_lines: want 1, got %d", note.Totals.HumanLines)
	}
	if note.Totals.AILines != 0 {
		t.Errorf("ai_lines: want 0, got %d", note.Totals.AILines)
	}
	if note.ByGenType.Human != 1 {
		t.Errorf("by_gen_type.human: want 1, got %d", note.ByGenType.Human)
	}
	if note.ByGenType.Chat != 0 {
		t.Errorf("by_gen_type.chat: want 0, got %d", note.ByGenType.Chat)
	}
}

// TestBuildNote_GenTypeCompletion verifies that lines recorded with
// gen_type=completion (e.g. Cursor Tab or Copilot Tab accepted via the editor
// plugin) are surfaced correctly in by_gen_type.completion, as AI lines in
// totals, and with gen_type="completion" in each per-line entry.
func TestBuildNote_GenTypeCompletion(t *testing.T) {
	db := openTestDB(t)
	repo := "/r"
	commitNanos := time.Now().UnixNano()

	_, err := db.InsertEdit(store.Edit{
		TimestampNanos: commitNanos - int64(5*time.Second),
		RepoPath:       repo,
		FilePath:       "bar.go",
		Tool:           store.ToolCursor,
		Confidence:     store.ConfidenceHigh,
		GenType:        store.GenTypeCompletion,
		Lines:          []store.EditLine{{StartLine: 1, EndLine: 3}},
	})
	if err != nil {
		t.Fatal(err)
	}

	added := []AddedLine{
		{File: "bar.go", LineNum: 1},
		{File: "bar.go", LineNum: 2},
		{File: "bar.go", LineNum: 3},
	}

	note, err := buildNote(db, repo, "abc123", commitNanos, added, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildNote: %v", err)
	}

	// All 3 lines: cursor/completion, not chat or human.
	if note.ByGenType.Completion != 3 {
		t.Errorf("by_gen_type.completion: want 3, got %d", note.ByGenType.Completion)
	}
	if note.ByGenType.Chat != 0 {
		t.Errorf("by_gen_type.chat: want 0, got %d", note.ByGenType.Chat)
	}
	if note.ByGenType.Human != 0 {
		t.Errorf("by_gen_type.human: want 0, got %d", note.ByGenType.Human)
	}
	if note.Totals.AILines != 3 {
		t.Errorf("ai_lines: want 3, got %d", note.Totals.AILines)
	}

	if len(note.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(note.Files))
	}
	for _, l := range expandLines(note.Files[0]) {
		if l.Type != "add" {
			continue
		}
		if l.AuthorType != "AI" {
			t.Errorf("line %d: AuthorType want AI, got %q", l.Line, l.AuthorType)
		}
		if l.GenType == nil || *l.GenType != "completion" {
			t.Errorf("line %d: GenType want 'completion', got %v", l.Line, l.GenType)
		}
	}

	// cursor should appear in by_tool with 3 lines.
	ct, ok := note.ByTool["cursor"]
	if !ok {
		t.Fatal("by_tool missing 'cursor' entry")
	}
	if ct.Lines != 3 {
		t.Errorf("by_tool[cursor].lines: want 3, got %d", ct.Lines)
	}
}

// TestBuildNote_ContentShaSurvivesUserInsert reproduces the line-drift bug: the
// AI Writes a file ("a","b","c"); the user later inserts a line in the middle,
// pushing the AI lines down. The AI lines must stay AI (matched by content_sha
// at their NEW positions) and the user's inserted line must be Human — NOT the
// inverse, which is what positional range matching produced before per-line
// content_sha + the content-only coversLine rule.
func TestBuildNote_ContentShaSurvivesUserInsert(t *testing.T) {
	db := openTestDB(t)
	repo := "/r"
	commitNanos := time.Now().UnixNano()

	// AI Write recorded per-line content_sha ranges at the ORIGINAL positions.
	_, err := db.InsertEdit(store.Edit{
		TimestampNanos: commitNanos - int64(5*time.Second),
		RepoPath:       repo,
		FilePath:       "app.go",
		Tool:           store.ToolClaude,
		Confidence:     store.ConfidenceHigh,
		GenType:        store.GenTypeChat,
		Lines: []store.EditLine{
			{StartLine: 1, EndLine: 1, ContentSHA: sha256HexStr([]byte("a"))},
			{StartLine: 2, EndLine: 2, ContentSHA: sha256HexStr([]byte("b"))},
			{StartLine: 3, EndLine: 3, ContentSHA: sha256HexStr([]byte("c"))},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// User inserted "USER" at position 2; AI lines b,c drifted to 3,4.
	added := []AddedLine{
		{File: "app.go", LineNum: 1, Content: "a"},
		{File: "app.go", LineNum: 2, Content: "USER"},
		{File: "app.go", LineNum: 3, Content: "b"},
		{File: "app.go", LineNum: 4, Content: "c"},
	}

	note, err := buildNote(db, repo, "abc123", commitNanos, added, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildNote: %v", err)
	}

	got := map[int]string{}
	for _, l := range expandLines(note.Files[0]) {
		if l.Type == "add" {
			got[l.Line] = l.AuthorType
		}
	}
	want := map[int]string{1: "AI", 2: "Human", 3: "AI", 4: "AI"}
	for ln, exp := range want {
		if got[ln] != exp {
			t.Errorf("line %d: AuthorType want %s, got %s", ln, exp, got[ln])
		}
	}
	if note.Totals.AILines != 3 || note.Totals.HumanLines != 1 {
		t.Errorf("totals: want AI=3 Human=1, got AI=%d Human=%d", note.Totals.AILines, note.Totals.HumanLines)
	}
}

// TestBuildNote_CopyPasteOfAIBlockIsHuman reproduces the copy-paste false
// positive: AI generates a 3-line block at lines 1-3 (content_sha recorded at
// those exact lines). In the SAME commit, the user copies that block and pastes
// a duplicate at lines 5-7. The original lines must stay AI; the pasted
// duplicate — same content, different lines — must attribute as Human even
// though its content_sha matches a recorded AI edit.
func TestBuildNote_CopyPasteOfAIBlockIsHuman(t *testing.T) {
	db := openTestDB(t)
	repo := "/r"
	commitNanos := time.Now().UnixNano()

	_, err := db.InsertEdit(store.Edit{
		TimestampNanos: commitNanos - int64(5*time.Second),
		RepoPath:       repo,
		FilePath:       "app.go",
		Tool:           store.ToolClaude,
		Confidence:     store.ConfidenceHigh,
		GenType:        store.GenTypeChat,
		Lines: []store.EditLine{
			{StartLine: 1, EndLine: 1, ContentSHA: sha256HexStr([]byte("a"))},
			{StartLine: 2, EndLine: 2, ContentSHA: sha256HexStr([]byte("b"))},
			{StartLine: 3, EndLine: 3, ContentSHA: sha256HexStr([]byte("c"))},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Lines 1-3 are the original AI block; lines 5-7 are a human copy-paste of
	// the same content at different positions (line 4 is an unrelated blank/sep).
	added := []AddedLine{
		{File: "app.go", LineNum: 1, Content: "a"},
		{File: "app.go", LineNum: 2, Content: "b"},
		{File: "app.go", LineNum: 3, Content: "c"},
		{File: "app.go", LineNum: 5, Content: "a"},
		{File: "app.go", LineNum: 6, Content: "b"},
		{File: "app.go", LineNum: 7, Content: "c"},
	}

	note, err := buildNote(db, repo, "abc123", commitNanos, added, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildNote: %v", err)
	}

	got := map[int]string{}
	for _, l := range expandLines(note.Files[0]) {
		if l.Type == "add" {
			got[l.Line] = l.AuthorType
		}
	}
	want := map[int]string{1: "AI", 2: "AI", 3: "AI", 5: "Human", 6: "Human", 7: "Human"}
	for ln, exp := range want {
		if got[ln] != exp {
			t.Errorf("line %d: AuthorType want %s, got %s", ln, exp, got[ln])
		}
	}
	if note.Totals.AILines != 3 || note.Totals.HumanLines != 3 {
		t.Errorf("totals: want AI=3 Human=3, got AI=%d Human=%d", note.Totals.AILines, note.Totals.HumanLines)
	}
}

// TestBuildNote_AutoformatterDriftNormalizedFallback reproduces the
// "AI generates code, then an autoformatter reflows it" bug: the AI's edit was
// recorded with content_sha for "\treturn 1" (tab indent), but an
// autoformatter rewrote the committed line as "    return 1" (space indent).
// The exact content_sha no longer matches, but content_sha_norm — computed
// from the whitespace-collapsed text — does, so the line must still
// attribute as AI rather than falling through to Human.
func TestBuildNote_AutoformatterDriftNormalizedFallback(t *testing.T) {
	db := openTestDB(t)
	repo := "/r"
	commitNanos := time.Now().UnixNano()

	_, err := db.InsertEdit(store.Edit{
		TimestampNanos: commitNanos - int64(5*time.Second),
		RepoPath:       repo,
		FilePath:       "app.go",
		Tool:           store.ToolClaude,
		Confidence:     store.ConfidenceHigh,
		GenType:        store.GenTypeChat,
		Lines: []store.EditLine{
			{StartLine: 1, EndLine: 1, ContentSHA: sha256HexStr([]byte("func foo() {")), ContentSHANorm: sha256HexNormStr("func foo() {")},
			{StartLine: 2, EndLine: 2, ContentSHA: sha256HexStr([]byte("\treturn 1")), ContentSHANorm: sha256HexNormStr("\treturn 1")},
			{StartLine: 3, EndLine: 3, ContentSHA: sha256HexStr([]byte("}")), ContentSHANorm: sha256HexNormStr("}")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Committed content: line 2 was reindented (tab -> spaces) by an
	// autoformatter after the AI wrote it.
	added := []AddedLine{
		{File: "app.go", LineNum: 1, Content: "func foo() {"},
		{File: "app.go", LineNum: 2, Content: "    return 1"},
		{File: "app.go", LineNum: 3, Content: "}"},
	}

	note, err := buildNote(db, repo, "abc123", commitNanos, added, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildNote: %v", err)
	}

	got := map[int]string{}
	for _, l := range expandLines(note.Files[0]) {
		if l.Type == "add" {
			got[l.Line] = l.AuthorType
		}
	}
	want := map[int]string{1: "AI", 2: "AI", 3: "AI"}
	for ln, exp := range want {
		if got[ln] != exp {
			t.Errorf("line %d: AuthorType want %s, got %s", ln, exp, got[ln])
		}
	}
	if note.Totals.AILines != 3 || note.Totals.HumanLines != 0 {
		t.Errorf("totals: want AI=3 Human=0, got AI=%d Human=%d", note.Totals.AILines, note.Totals.HumanLines)
	}
}

// TestBuildNote_NormalizedFallbackCopyPasteIsHuman is the
// content_sha_norm counterpart of TestBuildNote_CopyPasteOfAIBlockIsHuman: the
// AI's "\treturn 1" was reformatted to "    return 1" at line 1 (matches via
// content_sha_norm), and a human separately typed "        return 1" (a
// different indent, same normalized text) at line 3. The reformatted AI line
// must stay AI; the human's same-shape line must not also match via the
// normalized drift fallback.
func TestBuildNote_NormalizedFallbackCopyPasteIsHuman(t *testing.T) {
	db := openTestDB(t)
	repo := "/r"
	commitNanos := time.Now().UnixNano()

	_, err := db.InsertEdit(store.Edit{
		TimestampNanos: commitNanos - int64(5*time.Second),
		RepoPath:       repo,
		FilePath:       "app.go",
		Tool:           store.ToolClaude,
		Confidence:     store.ConfidenceHigh,
		GenType:        store.GenTypeChat,
		Lines: []store.EditLine{
			{StartLine: 1, EndLine: 1, ContentSHA: sha256HexStr([]byte("\treturn 1")), ContentSHANorm: sha256HexNormStr("\treturn 1")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	added := []AddedLine{
		{File: "app.go", LineNum: 1, Content: "    return 1"},     // autoformatted AI line
		{File: "app.go", LineNum: 3, Content: "        return 1"}, // human-typed, same normalized shape
	}

	note, err := buildNote(db, repo, "abc123", commitNanos, added, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildNote: %v", err)
	}

	got := map[int]string{}
	for _, l := range expandLines(note.Files[0]) {
		if l.Type == "add" {
			got[l.Line] = l.AuthorType
		}
	}
	want := map[int]string{1: "AI", 3: "Human"}
	for ln, exp := range want {
		if got[ln] != exp {
			t.Errorf("line %d: AuthorType want %s, got %s", ln, exp, got[ln])
		}
	}
	if note.Totals.AILines != 1 || note.Totals.HumanLines != 1 {
		t.Errorf("totals: want AI=1 Human=1, got AI=%d Human=%d", note.Totals.AILines, note.Totals.HumanLines)
	}
}

// TestBuildNote_BlankLineInheritsAIBlock reproduces the "blank lines flip to
// Human after the user edits" bug: the AI wrote a block whose blank lines have
// no (unique) content_sha, so after the file drifts they can't be content-
// matched and default to Human. They must inherit AI from the surrounding block
// rather than be mislabelled human. The user's own non-blank line stays Human.
func TestBuildNote_BlankLineInheritsAIBlock(t *testing.T) {
	db := openTestDB(t)
	repo := "/r"
	commitNanos := time.Now().UnixNano()

	// AI wrote non-blank lines "a" and "b" (content_sha). The blank line between
	// them carries no content_sha and won't match by content.
	_, err := db.InsertEdit(store.Edit{
		TimestampNanos: commitNanos - int64(5*time.Second),
		RepoPath:       repo,
		FilePath:       "page.html",
		Tool:           store.ToolClaude,
		Confidence:     store.ConfidenceHigh,
		GenType:        store.GenTypeChat,
		Lines: []store.EditLine{
			{StartLine: 1, EndLine: 1, ContentSHA: sha256HexStr([]byte("a"))},
			{StartLine: 3, EndLine: 3, ContentSHA: sha256HexStr([]byte("b"))},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Line 2 is blank (no edit match); line 4 is the user's own added code.
	added := []AddedLine{
		{File: "page.html", LineNum: 1, Content: "a"},
		{File: "page.html", LineNum: 2, Content: ""},
		{File: "page.html", LineNum: 3, Content: "b"},
		{File: "page.html", LineNum: 4, Content: "user();"},
	}

	note, err := buildNote(db, repo, "abc", commitNanos, added, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildNote: %v", err)
	}
	got := map[int]string{}
	for _, l := range expandLines(note.Files[0]) {
		if l.Type == "add" {
			got[l.Line] = l.AuthorType
		}
	}
	want := map[int]string{1: "AI", 2: "AI", 3: "AI", 4: "Human"}
	for ln, exp := range want {
		if got[ln] != exp {
			t.Errorf("line %d: AuthorType want %s, got %s", ln, exp, got[ln])
		}
	}
}

// TestBuildNote_BlankLineStaysHumanInHumanBlock confirms the inheritance is
// directional: a blank line surrounded by human-typed lines stays Human (it is
// not promoted to AI just because an AI edit exists elsewhere).
func TestBuildNote_BlankLineStaysHumanInHumanBlock(t *testing.T) {
	db := openTestDB(t)
	repo := "/r"
	commitNanos := time.Now().UnixNano()
	added := []AddedLine{
		{File: "h.txt", LineNum: 1, Content: "x"},
		{File: "h.txt", LineNum: 2, Content: ""},
		{File: "h.txt", LineNum: 3, Content: "y"},
	}
	note, err := buildNote(db, repo, "abc", commitNanos, added, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildNote: %v", err)
	}
	for _, l := range expandLines(note.Files[0]) {
		if l.Type == "add" && l.AuthorType != "Human" {
			t.Errorf("line %d: want Human, got %s", l.Line, l.AuthorType)
		}
	}
}

// TestBuildNote_MixedCommit_AIAndHumanInSameCommit verifies that a single
// commit where the AI wrote some lines and the human typed others correctly
// splits attribution. AI lines have explicit DB records; human lines have no
// record and fall to the human default (tool="", gen_type=human).
func TestBuildNote_MixedCommit_AIAndHumanInSameCommit(t *testing.T) {
	db := openTestDB(t)
	repo := "/r"
	commitNanos := time.Now().UnixNano()

	// Cursor Composer wrote lines 1-3 (chat). Lines 4-5 have no record — human.
	_, err := db.InsertEdit(store.Edit{
		TimestampNanos: commitNanos - int64(30*time.Second),
		RepoPath:       repo,
		FilePath:       "mix.go",
		Tool:           store.ToolCursor,
		Confidence:     store.ConfidenceHigh,
		GenType:        store.GenTypeChat,
		Lines:          []store.EditLine{{StartLine: 1, EndLine: 3}},
	})
	if err != nil {
		t.Fatal(err)
	}

	added := []AddedLine{
		{File: "mix.go", LineNum: 1},
		{File: "mix.go", LineNum: 2},
		{File: "mix.go", LineNum: 3},
		{File: "mix.go", LineNum: 4},
		{File: "mix.go", LineNum: 5},
	}

	note, err := buildNote(db, repo, "mixed01", commitNanos, added, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildNote: %v", err)
	}

	if note.Totals.AILines != 3 {
		t.Errorf("ai_lines: want 3, got %d", note.Totals.AILines)
	}
	if note.Totals.HumanLines != 2 {
		t.Errorf("human_lines: want 2, got %d", note.Totals.HumanLines)
	}
	if note.ByGenType.Chat != 3 {
		t.Errorf("by_gen_type.chat: want 3, got %d", note.ByGenType.Chat)
	}
	if note.ByGenType.Human != 2 {
		t.Errorf("by_gen_type.human: want 2, got %d", note.ByGenType.Human)
	}

	if len(note.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(note.Files))
	}
	for _, l := range expandLines(note.Files[0]) {
		if l.Type != "add" {
			continue
		}
		switch {
		case l.Line <= 3:
			if l.AuthorType != "AI" || l.Tool != "cursor" {
				t.Errorf("line %d: want AI/cursor, got AuthorType=%q Tool=%q", l.Line, l.AuthorType, l.Tool)
			}
			if l.GenType == nil || *l.GenType != "chat" {
				t.Errorf("line %d: want gen_type=chat, got %v", l.Line, l.GenType)
			}
		case l.Line >= 4:
			if l.AuthorType != "Human" || l.Tool != "" {
				t.Errorf("line %d: want Human/'', got AuthorType=%q Tool=%q", l.Line, l.AuthorType, l.Tool)
			}
			if l.GenType == nil || *l.GenType != "human" {
				t.Errorf("line %d: want gen_type=human, got %v", l.Line, l.GenType)
			}
		}
	}
}

func TestBuildNote_DeletionOnlyCommit(t *testing.T) {
	db := openTestDB(t)
	commitNanos := time.Now().UnixNano()

	// No added lines at all; only deletions on foo.go.
	added := []AddedLine{}
	deleted := map[string][]DeletedLine{
		"foo.go": {{LineNum: 2, Content: "old line 2"}, {LineNum: 4, Content: "old line 4"}},
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
	lines := expandLines(f)
	if len(lines) != 2 {
		t.Fatalf("expected 2 deleted lines, got %d", len(lines))
	}
	for _, l := range lines {
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

// TestBuildNote_AIDeletedLine_MatchedByContentSHA covers the core
// AI-deletion-attribution case: a Claude CLI edit recorded both an added line
// (line 1, via content_sha) and a removed line's content_sha
// (edit_removed_lines). The committed diff has two deletions: one whose
// content matches the recorded removed-line hash (→ AuthorType "AI",
// Tool "claude", gen_type "cli") and one that doesn't (→ stays "Human", no
// regression).
func TestBuildNote_AIDeletedLine_MatchedByContentSHA(t *testing.T) {
	db := openTestDB(t)
	repo := "/r"
	commitNanos := time.Now().UnixNano()

	const removedContent = "func oldHelper() {}"
	const addedContent = "func newHelper() {}"

	_, err := db.InsertEdit(store.Edit{
		TimestampNanos: commitNanos - int64(10*time.Second),
		RepoPath:       repo,
		FilePath:       "foo.go",
		Tool:           store.ToolClaude,
		Confidence:     store.ConfidenceHigh,
		GenType:        store.GenTypeCLI,
		Lines:          []store.EditLine{{StartLine: 1, EndLine: 1, ContentSHA: sha256HexStr([]byte(addedContent))}},
		RemovedLines: []store.RemovedLineHash{
			{ContentSHA: sha256HexStr([]byte(removedContent))},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	added := []AddedLine{
		{File: "foo.go", LineNum: 1, Content: addedContent},
	}
	deleted := map[string][]DeletedLine{
		"foo.go": {
			{LineNum: 10, Content: removedContent},     // matches the AI edit's removed-line hash
			{LineNum: 11, Content: "manually removed"}, // no recorded match -> stays Human
		},
	}

	note, err := buildNote(db, repo, "deadbeef", commitNanos, added, deleted, nil, nil)
	if err != nil {
		t.Fatalf("buildNote: %v", err)
	}

	if len(note.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(note.Files))
	}
	lines := expandLines(note.Files[0])

	var aiDel, humanDel *LineEntry
	for i := range lines {
		if lines[i].Type != "delete" {
			continue
		}
		switch lines[i].Line {
		case 10:
			aiDel = &lines[i]
		case 11:
			humanDel = &lines[i]
		}
	}
	if aiDel == nil {
		t.Fatal("no delete entry for line 10")
	}
	if aiDel.AuthorType != "AI" || aiDel.Tool != "claude" {
		t.Errorf("line 10: want AI/claude, got AuthorType=%q Tool=%q", aiDel.AuthorType, aiDel.Tool)
	}
	if aiDel.GenType == nil || *aiDel.GenType != "cli" {
		t.Errorf("line 10: want gen_type=cli, got %v", aiDel.GenType)
	}
	if humanDel == nil {
		t.Fatal("no delete entry for line 11")
	}
	if humanDel.AuthorType != "Human" || humanDel.Tool != "" {
		t.Errorf("line 11: want Human/'', got AuthorType=%q Tool=%q", humanDel.AuthorType, humanDel.Tool)
	}
	if humanDel.GenType != nil {
		t.Errorf("line 11: want gen_type unset, got %v", *humanDel.GenType)
	}

	if note.Totals.AIDeletedLines != 1 {
		t.Errorf("ai_deleted_lines: want 1, got %d", note.Totals.AIDeletedLines)
	}
	if note.Totals.DeletedLines != 2 {
		t.Errorf("deleted_lines: want 2, got %d", note.Totals.DeletedLines)
	}
	// by_gen_type.cli: 1 from the added line + 1 from the AI-attributed delete.
	if note.ByGenType.CLI != 2 {
		t.Errorf("by_gen_type.cli: want 2, got %d", note.ByGenType.CLI)
	}
	if note.ByGenType.Human != 1 {
		t.Errorf("by_gen_type.human: want 1, got %d", note.ByGenType.Human)
	}
}

// TestBuildNote_AIDeletedLine_PureDeletionFile covers the same matching for a
// file with ONLY deletions (no added lines), exercised via the pure-deletion
// FileEntry path rather than flushFile.
func TestBuildNote_AIDeletedLine_PureDeletionFile(t *testing.T) {
	db := openTestDB(t)
	repo := "/r"
	commitNanos := time.Now().UnixNano()

	const removedContent = "deprecated.Call()"

	_, err := db.InsertEdit(store.Edit{
		TimestampNanos: commitNanos - int64(10*time.Second),
		RepoPath:       repo,
		FilePath:       "foo.go",
		Tool:           store.ToolCodex,
		Confidence:     store.ConfidenceHigh,
		GenType:        store.GenTypeCLI,
		RemovedLines: []store.RemovedLineHash{
			{ContentSHA: sha256HexStr([]byte(removedContent))},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	deleted := map[string][]DeletedLine{
		"foo.go": {{LineNum: 5, Content: removedContent}},
	}

	note, err := buildNote(db, repo, "deadbeef", commitNanos, nil, deleted, nil, nil)
	if err != nil {
		t.Fatalf("buildNote: %v", err)
	}

	if len(note.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(note.Files))
	}
	lines := expandLines(note.Files[0])
	if len(lines) != 1 || lines[0].Type != "delete" {
		t.Fatalf("expected 1 delete line, got %+v", lines)
	}
	if lines[0].AuthorType != "AI" || lines[0].Tool != "codex" {
		t.Errorf("want AI/codex, got AuthorType=%q Tool=%q", lines[0].AuthorType, lines[0].Tool)
	}
	if note.Totals.AIDeletedLines != 1 {
		t.Errorf("ai_deleted_lines: want 1, got %d", note.Totals.AIDeletedLines)
	}
	if note.ByGenType.CLI != 1 {
		t.Errorf("by_gen_type.cli: want 1, got %d", note.ByGenType.CLI)
	}
}

// TestBuildNote_AIDeletedBlankLine_InheritsNeighborAttribution covers the
// "kerim" commit bug report: an AI removed a contiguous block of lines that
// included blank lines. tools.RemovedLineHashes never records a content_sha
// for blank lines, so pickEditForRemovedLine can never match the blank
// deleted lines directly — without inheritBlankDeletedLineAttribution they'd
// stay "Human" even though the whole block came from one AI edit.
func TestBuildNote_AIDeletedBlankLine_InheritsNeighborAttribution(t *testing.T) {
	db := openTestDB(t)
	repo := "/r"
	commitNanos := time.Now().UnixNano()

	const lineA = "func A() {}"
	const lineB = "func B() {}"

	_, err := db.InsertEdit(store.Edit{
		TimestampNanos: commitNanos - int64(10*time.Second),
		RepoPath:       repo,
		FilePath:       "foo.go",
		Tool:           store.ToolClaude,
		Confidence:     store.ConfidenceHigh,
		GenType:        store.GenTypeChat,
		RemovedLines: []store.RemovedLineHash{
			{ContentSHA: sha256HexStr([]byte(lineA))},
			{ContentSHA: sha256HexStr([]byte(lineB))},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	deleted := map[string][]DeletedLine{
		"foo.go": {
			{LineNum: 10, Content: lineA},
			{LineNum: 11, Content: ""}, // blank line in the middle of the AI's removal
			{LineNum: 12, Content: lineB},
		},
	}

	note, err := buildNote(db, repo, "deadbeef", commitNanos, nil, deleted, nil, nil)
	if err != nil {
		t.Fatalf("buildNote: %v", err)
	}

	if len(note.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(note.Files))
	}
	lines := expandLines(note.Files[0])
	if len(lines) != 3 {
		t.Fatalf("expected 3 delete lines, got %+v", lines)
	}
	for _, l := range lines {
		if l.AuthorType != "AI" || l.Tool != "claude" {
			t.Errorf("line %d: want AI/claude, got AuthorType=%q Tool=%q", l.Line, l.AuthorType, l.Tool)
		}
		if l.GenType == nil || *l.GenType != "chat" {
			t.Errorf("line %d: want gen_type=chat, got %v", l.Line, l.GenType)
		}
	}
	if note.Totals.AIDeletedLines != 3 {
		t.Errorf("ai_deleted_lines: want 3 (blank line inherits), got %d", note.Totals.AIDeletedLines)
	}
	if note.Totals.DeletedLines != 3 {
		t.Errorf("deleted_lines: want 3, got %d", note.Totals.DeletedLines)
	}
	if note.ByGenType.Chat != 3 {
		t.Errorf("by_gen_type.chat: want 3, got %d", note.ByGenType.Chat)
	}
	if note.ByGenType.Human != 0 {
		t.Errorf("by_gen_type.human: want 0, got %d", note.ByGenType.Human)
	}
}

// TestBuildNote_DeletedBlankLine_HumanNeighborStaysHuman ensures the
// inheritance pass mirrors inheritBlankLineAttribution's "look backward
// first" rule: a blank deleted line whose nearest non-blank neighbour is a
// Human deletion stays "Human", even when an AI-attributed deletion sits
// further away on the other side.
func TestBuildNote_DeletedBlankLine_HumanNeighborStaysHuman(t *testing.T) {
	db := openTestDB(t)
	repo := "/r"
	commitNanos := time.Now().UnixNano()

	const lineB = "func B() {}"

	_, err := db.InsertEdit(store.Edit{
		TimestampNanos: commitNanos - int64(10*time.Second),
		RepoPath:       repo,
		FilePath:       "foo.go",
		Tool:           store.ToolClaude,
		Confidence:     store.ConfidenceHigh,
		GenType:        store.GenTypeChat,
		RemovedLines: []store.RemovedLineHash{
			{ContentSHA: sha256HexStr([]byte(lineB))},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	deleted := map[string][]DeletedLine{
		"foo.go": {
			{LineNum: 10, Content: "manually removed"}, // no recorded match -> Human
			{LineNum: 11, Content: ""},                 // blank: backward neighbor (10) is Human
			{LineNum: 12, Content: lineB},              // AI-attributed
		},
	}

	note, err := buildNote(db, repo, "deadbeef", commitNanos, nil, deleted, nil, nil)
	if err != nil {
		t.Fatalf("buildNote: %v", err)
	}

	lines := expandLines(note.Files[0])
	if len(lines) != 3 {
		t.Fatalf("expected 3 delete lines, got %+v", lines)
	}
	for _, l := range lines {
		switch l.Line {
		case 10, 11:
			if l.AuthorType != "Human" || l.Tool != "" {
				t.Errorf("line %d: want Human/'', got AuthorType=%q Tool=%q", l.Line, l.AuthorType, l.Tool)
			}
		case 12:
			if l.AuthorType != "AI" || l.Tool != "claude" {
				t.Errorf("line %d: want AI/claude, got AuthorType=%q Tool=%q", l.Line, l.AuthorType, l.Tool)
			}
		}
	}
	if note.Totals.AIDeletedLines != 1 {
		t.Errorf("ai_deleted_lines: want 1, got %d", note.Totals.AIDeletedLines)
	}
}
