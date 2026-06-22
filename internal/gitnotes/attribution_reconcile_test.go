package gitnotes

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blamely/blamely/internal/store"
)

// editLines turns raw committed line text into the per-line EditLine records a
// chat watcher would write: content_sha/content_sha_norm of each NON-BLANK line,
// at PLACEHOLDER positions (1..N) — exactly the shape that defeats the whole-file
// working-log fold. Blank lines are skipped, mirroring the real recorder
// (tools.copilotAddedRangesFromContent), so they must be attributed by inheritance.
func editLines(texts ...string) []store.EditLine {
	out := make([]store.EditLine, 0, len(texts))
	pos := 0
	for _, s := range texts {
		pos++
		if strings.TrimSpace(s) == "" {
			continue
		}
		out = append(out, store.EditLine{
			StartLine: pos, EndLine: pos,
			ContentSHA: sha256HexStr([]byte(s)), ContentSHANorm: sha256HexNormStr(s),
		})
	}
	return out
}

func addRange(fe *FileEntry, start, end int, author, tool, gen string) {
	r := RangeEntry{Start: start, End: end, Type: "add", AuthorType: author, Tool: tool}
	if gen != "" {
		g := gen
		r.GenType = &g
	}
	fe.Lines = append(fe.Lines, r)
}

// addRangeAuthor reports the attribution of the single-line add range covering ln.
func addRangeAuthor(t *testing.T, note *Note, path string, ln int) RangeEntry {
	t.Helper()
	for fi := range note.Files {
		if note.Files[fi].Path != path {
			continue
		}
		for _, r := range note.Files[fi].Lines {
			if r.Type == "add" && r.Start <= ln && ln <= r.End {
				return r
			}
		}
	}
	t.Fatalf("no add range covering %s:%d", path, ln)
	return RangeEntry{}
}

// TestReconcileAddsFromEdits_DuplicateContentBlock reproduces the okta-block bug
// from commit a636614c: an AI chat tool generates a new block whose lines are
// byte-identical to a pre-existing sibling block. The whole-file working-log fold
// can't tell which copy is the AI's, so it leaves the new lines Human. The
// recorded content_shas (placeholder positions) reattribute them deterministically.
func TestReconcileAddsFromEdits_DuplicateContentBlock(t *testing.T) {
	db, err := store.OpenAt(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer db.Close()

	const repo = "/repo"
	const file = "login.html"

	// The AI-generated okta block. Its middle lines duplicate the pre-existing
	// github block elsewhere in the file (display:flex, color:#fff) — only the
	// selector and colour are distinctive.
	okta := []string{"    .okta-btn {", "      display: flex;", "      color: #fff;", "    }", "", "    .okta-icon {", "      width: 18px;", "    }"}

	if _, err := db.InsertEdit(store.Edit{
		TimestampNanos: 1000,
		RepoPath:       repo,
		FilePath:       file,
		Tool:           "copilot",
		Confidence:     "high",
		GenType:        "chat",
		Model:          sql.NullString{String: "gpt-5-mini", Valid: true},
		Lines:          editLines(okta...),
	}); err != nil {
		t.Fatalf("InsertEdit: %v", err)
	}

	// What the working-log flip produced: the okta block (committed lines 114-121,
	// including the blank separator at 118) wrongly left Human; a hand-typed line
	// (190) genuinely Human; an inline completion (163) the working log DID catch
	// as AI — must be preserved.
	fe := &FileEntry{Path: file}
	addRange(fe, 114, 121, "Human", "", "")
	addRange(fe, 163, 163, "AI", "copilot", "completion")
	addRange(fe, 190, 190, "Human", "", "")
	note := &Note{Schema: 2, Files: []FileEntry{*fe}}

	added := []AddedLine{
		{File: file, LineNum: 114, Content: okta[0]},
		{File: file, LineNum: 115, Content: okta[1]},
		{File: file, LineNum: 116, Content: okta[2]},
		{File: file, LineNum: 117, Content: okta[3]},
		{File: file, LineNum: 118, Content: okta[4]}, // blank separator
		{File: file, LineNum: 119, Content: okta[5]},
		{File: file, LineNum: 120, Content: okta[6]},
		{File: file, LineNum: 121, Content: okta[7]},
		{File: file, LineNum: 163, Content: "  <h1>Login</h1>"},
		{File: file, LineNum: 190, Content: "  <tr>kerim atik</tr>"},
	}

	reconcileAddsFromEdits(db, repo, 2000, note, added)

	// Every okta line — including the blank separator at 118 (inherited) — is now
	// AI/copilot/chat.
	for ln := 114; ln <= 121; ln++ {
		r := addRangeAuthor(t, note, file, ln)
		if r.AuthorType != "AI" || r.Tool != "copilot" || ptrStr(r.GenType) != "chat" {
			t.Errorf("line %d: want AI/copilot/chat, got author=%q tool=%q gen=%q",
				ln, r.AuthorType, r.Tool, ptrStr(r.GenType))
		}
	}
	// The completion line is untouched (no downgrade, no double-claim).
	if r := addRangeAuthor(t, note, file, 163); r.AuthorType != "AI" || ptrStr(r.GenType) != "completion" {
		t.Errorf("line 163: completion must be preserved, got author=%q gen=%q", r.AuthorType, ptrStr(r.GenType))
	}
	// The hand-typed line has no recorded AI hash and stays Human.
	if r := addRangeAuthor(t, note, file, 190); r.AuthorType != "Human" {
		t.Errorf("line 190: want Human, got %q", r.AuthorType)
	}

	// Aggregates were recomputed: 9 AI adds (8 okta + 1 completion), 1 human.
	if note.Totals.AILines != 9 || note.Totals.HumanLines != 1 {
		t.Errorf("totals: want AI=9 Human=1, got AI=%d Human=%d", note.Totals.AILines, note.Totals.HumanLines)
	}
	if note.ByGenType.Chat != 8 || note.ByGenType.Completion != 1 || note.ByGenType.Human != 1 {
		t.Errorf("by_gen_type: want chat=8 completion=1 human=1, got %+v", note.ByGenType)
	}
	if note.ByTool["copilot"].Lines != 9 {
		t.Errorf("by_tool copilot lines: want 9, got %d", note.ByTool["copilot"].Lines)
	}
}

// TestReconcileAddsFromEdits_IsolatedBlankNotInherited reproduces commit 095f6221:
// a lone blank added line, separated from any AI-added block by UNCHANGED lines,
// must NOT inherit a distant AI block's attribution. Only blanks contiguous with an
// AI block inherit.
func TestReconcileAddsFromEdits_IsolatedBlankNotInherited(t *testing.T) {
	db, err := store.OpenAt(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer db.Close()

	const repo, file = "/repo", "scenario.txt"
	// An AI completion recorded a distinct line that lands at committed line 13.
	aiLine := "generate dummy test 4"
	if _, err := db.InsertEdit(store.Edit{
		TimestampNanos: 1000, RepoPath: repo, FilePath: file,
		Tool: "copilot", Confidence: "high", GenType: "completion",
		Lines: editLines(aiLine),
	}); err != nil {
		t.Fatalf("InsertEdit: %v", err)
	}

	// Two added lines: a lone blank at 4 (surrounded by unchanged 3 and 5), and the
	// AI line at 13. They are NOT contiguous.
	fe := &FileEntry{Path: file}
	addRange(fe, 4, 4, "Human", "", "")
	addRange(fe, 13, 13, "Human", "", "")
	note := &Note{Schema: 2, Files: []FileEntry{*fe}}
	added := []AddedLine{
		{File: file, LineNum: 4, Content: " "}, // blank
		{File: file, LineNum: 13, Content: aiLine},
	}

	reconcileAddsFromEdits(db, repo, 2000, note, added)

	if r := addRangeAuthor(t, note, file, 13); r.AuthorType != "AI" {
		t.Errorf("line 13: want AI (direct content match), got %q", r.AuthorType)
	}
	if r := addRangeAuthor(t, note, file, 4); r.AuthorType != "Human" {
		t.Errorf("line 4: isolated blank must stay Human, not inherit line 13, got %s/%s",
			r.AuthorType, ptrStr(r.GenType))
	}
}

// TestReconcileAddsFromEdits_CopyPasteTag verifies a copy-pasted line is tagged
// tool=copypaste while staying author_type Human (the AI/Human split is unchanged),
// and shows up in by_tool. Repro of commit ae00d8ad.
func TestReconcileAddsFromEdits_CopyPasteTag(t *testing.T) {
	db, err := store.OpenAt(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer db.Close()

	const repo, file = "/repo", "scenario.txt"
	pasted := "generate dummy test 1"
	if _, err := db.InsertEdit(store.Edit{
		TimestampNanos: 1000, RepoPath: repo, FilePath: file,
		Tool: "copypaste", Confidence: "high", GenType: "human",
		Lines: editLines(pasted),
	}); err != nil {
		t.Fatalf("InsertEdit: %v", err)
	}

	fe := &FileEntry{Path: file}
	addRange(fe, 12, 12, "Human", "", "")
	addRange(fe, 13, 13, "Human", "", "") // a typed line, not pasted
	note := &Note{Schema: 2, Files: []FileEntry{*fe}}
	added := []AddedLine{
		{File: file, LineNum: 12, Content: pasted},
		{File: file, LineNum: 13, Content: "typed by hand"},
	}

	reconcileAddsFromEdits(db, repo, 2000, note, added)

	r := addRangeAuthor(t, note, file, 12)
	if r.AuthorType != "Human" || r.Tool != "copypaste" {
		t.Errorf("line 12: want Human/copypaste, got %s/%s", r.AuthorType, r.Tool)
	}
	if r := addRangeAuthor(t, note, file, 13); r.AuthorType != "Human" || r.Tool != "" {
		t.Errorf("line 13: want untagged Human, got %s/%s", r.AuthorType, r.Tool)
	}
	// AI/Human split unaffected; copypaste appears in by_tool.
	if note.Totals.AILines != 0 || note.Totals.HumanLines != 2 {
		t.Errorf("split: want AI=0 Human=2, got AI=%d Human=%d", note.Totals.AILines, note.Totals.HumanLines)
	}
	if note.ByTool["copypaste"].Lines != 1 {
		t.Errorf("by_tool copypaste: want 1 line, got %d", note.ByTool["copypaste"].Lines)
	}
}

// TestReconcileAddsFromEdits_ConsumeOnce verifies an AI edit that recorded a line
// ONCE can only attribute one committed copy of that line; a second identical
// added line stays Human (we never over-credit beyond what the AI recorded).
func TestReconcileAddsFromEdits_ConsumeOnce(t *testing.T) {
	db, err := store.OpenAt(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer db.Close()

	const repo, file = "/repo", "f.css"
	dup := "      gap: 0.5rem;"
	if _, err := db.InsertEdit(store.Edit{
		TimestampNanos: 1000, RepoPath: repo, FilePath: file,
		Tool: "claude", Confidence: "high", GenType: "chat",
		Lines: editLines(dup), // recorded exactly once
	}); err != nil {
		t.Fatalf("InsertEdit: %v", err)
	}

	fe := &FileEntry{Path: file}
	addRange(fe, 10, 11, "Human", "", "") // two identical added lines
	note := &Note{Schema: 2, Files: []FileEntry{*fe}}
	added := []AddedLine{
		{File: file, LineNum: 10, Content: dup},
		{File: file, LineNum: 11, Content: dup},
	}

	reconcileAddsFromEdits(db, repo, 2000, note, added)

	a10 := addRangeAuthor(t, note, file, 10).AuthorType
	a11 := addRangeAuthor(t, note, file, 11).AuthorType
	if !((a10 == "AI" && a11 == "Human") || (a10 == "Human" && a11 == "AI")) {
		t.Errorf("consume-once: exactly one line should be AI, got %q and %q", a10, a11)
	}
}

// TestReconcileAddsFromEdits_SkipsWholeFileWrite verifies a human-typed line that a
// claude Write (narrowed against a possibly-stale snapshot) re-emitted is NOT flipped
// to AI — the Write's content_shas are excluded. Repro of commit ac7c0c3b lines 14-15.
func TestReconcileAddsFromEdits_SkipsWholeFileWrite(t *testing.T) {
	db, err := store.OpenAt(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer db.Close()

	const repo, file = "/repo", "scenario.txt"
	human := "kerim atik"
	// A claude Write recorded the human's line (raw_meta tool=Write).
	if _, err := db.InsertEdit(store.Edit{
		TimestampNanos: 1000, RepoPath: repo, FilePath: file,
		Tool: "claude", Confidence: "high", GenType: "chat",
		RawMeta: sql.NullString{String: `{"session_id":"s","tool":"Write","transcript_path":"t"}`, Valid: true},
		Lines:   editLines(human),
	}); err != nil {
		t.Fatalf("InsertEdit: %v", err)
	}

	fe := &FileEntry{Path: file}
	addRange(fe, 14, 14, "Human", "", "")
	note := &Note{Schema: 2, Files: []FileEntry{*fe}}
	added := []AddedLine{{File: file, LineNum: 14, Content: human}}

	reconcileAddsFromEdits(db, repo, 2000, note, added)

	if r := addRangeAuthor(t, note, file, 14); r.AuthorType != "Human" {
		t.Errorf("line 14: a whole-file Write must not flip a human line to AI, got %q", r.AuthorType)
	}

	// Control: the SAME content recorded by a focused chat edit (no Write marker) IS
	// reconciled — proving it's the Write marker, not the content, that's excluded.
	if _, err := db.InsertEdit(store.Edit{
		TimestampNanos: 1100, RepoPath: repo, FilePath: file,
		Tool: "claude", Confidence: "high", GenType: "chat",
		RawMeta: sql.NullString{String: `{"session_id":"s","tool":"Edit","transcript_path":"t"}`, Valid: true},
		Lines:   editLines(human),
	}); err != nil {
		t.Fatalf("InsertEdit: %v", err)
	}
	fe2 := &FileEntry{Path: file}
	addRange(fe2, 14, 14, "Human", "", "")
	note2 := &Note{Schema: 2, Files: []FileEntry{*fe2}}
	reconcileAddsFromEdits(db, repo, 2000, note2, added)
	if r := addRangeAuthor(t, note2, file, 14); r.AuthorType != "AI" {
		t.Errorf("control: a focused Edit SHOULD reconcile the line to AI, got %q", r.AuthorType)
	}
}

// TestReconcileAddsFromEdits_NoFalsePositive verifies a human-typed line whose
// content was never recorded by any AI tool is left Human.
func TestReconcileAddsFromEdits_NoFalsePositive(t *testing.T) {
	db, err := store.OpenAt(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer db.Close()

	const repo, file = "/repo", "f.go"
	// An AI edit exists for the file but recorded DIFFERENT content.
	if _, err := db.InsertEdit(store.Edit{
		TimestampNanos: 1000, RepoPath: repo, FilePath: file,
		Tool: "copilot", Confidence: "high", GenType: "chat",
		Lines: editLines("aiOnly := true"),
	}); err != nil {
		t.Fatalf("InsertEdit: %v", err)
	}

	fe := &FileEntry{Path: file}
	addRange(fe, 5, 5, "Human", "", "")
	note := &Note{Schema: 2, Files: []FileEntry{*fe}}
	added := []AddedLine{{File: file, LineNum: 5, Content: "humanTyped := 1"}}

	reconcileAddsFromEdits(db, repo, 2000, note, added)

	if r := addRangeAuthor(t, note, file, 5); r.AuthorType != "Human" {
		t.Errorf("line 5: want Human (no matching AI hash), got %q", r.AuthorType)
	}
}
