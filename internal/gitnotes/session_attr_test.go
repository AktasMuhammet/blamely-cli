package gitnotes

import (
	"database/sql"
	"testing"
	"time"

	"github.com/blamely/blamely/internal/store"
)

// TestBuildNote_CrossSessionContentShaIsHuman verifies that an AI edit recorded
// under a DIFFERENT work session (e.g. a previous session, or the source branch
// of a cherry-pick) is NOT attributed to AI when its content lands in a new
// commit at a different line. content_sha matching is scoped to the CURRENT
// session: a human pasting previously AI-generated code (from an earlier
// session) into the current session's commit must attribute as Human.
func TestBuildNote_CrossSessionContentShaIsHuman(t *testing.T) {
	db := openTestDB(t)
	repo := "/r"
	now := time.Now().UnixNano()

	const lineText = "    return doWork()"
	// AI edit recorded long ago, in some other session (id 99), carrying the
	// content_sha for the line. buildNote here resolves session 0 (no git repo),
	// so this edit is NOT in the current (session 0 / NULL) set.
	_, err := db.InsertEdit(store.Edit{
		TimestampNanos: now - int64(10*time.Minute),
		RepoPath:       repo,
		FilePath:       "foo.go",
		Tool:           store.ToolClaude,
		Confidence:     store.ConfidenceHigh,
		GenType:        store.GenTypeChat,
		SessionID:      sql.NullString{Valid: true, String: "00000000-0000-4000-8000-000000000099"},
		Lines: []store.EditLine{
			{StartLine: 42, EndLine: 42, ContentSHA: sha256HexStr([]byte(lineText))},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The new commit adds the SAME content at a DIFFERENT line number.
	added := []AddedLine{{File: "foo.go", LineNum: 7, Content: lineText}}

	note, err := buildNote(db, repo, "newsha", now, added, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildNote: %v", err)
	}
	if len(note.Files) != 1 || len(note.Files[0].Lines) != 1 {
		t.Fatalf("expected 1 file/1 line, got %+v", note.Files)
	}
	l := note.Files[0].Lines[0]
	if l.AuthorType != "Human" {
		t.Errorf("cross-session content match: AuthorType want Human, got %q", l.AuthorType)
	}
	if l.Tool != "" {
		t.Errorf("cross-session content match: Tool want \"\", got %q", l.Tool)
	}
	if note.Totals.AILines != 0 {
		t.Errorf("ai_lines: want 0, got %d", note.Totals.AILines)
	}
}

// TestBuildNote_FallbackRequiresContentMatch ensures the cross-session fallback
// does NOT attribute by line number alone: a different-session edit whose content
// no longer matches (human overrode it) must fall to Human.
func TestBuildNote_FallbackRequiresContentMatch(t *testing.T) {
	db := openTestDB(t)
	repo := "/r"
	now := time.Now().UnixNano()

	_, err := db.InsertEdit(store.Edit{
		TimestampNanos: now - int64(10*time.Minute),
		RepoPath:       repo,
		FilePath:       "foo.go",
		Tool:           store.ToolClaude,
		Confidence:     store.ConfidenceHigh,
		GenType:        store.GenTypeChat,
		SessionID:      sql.NullString{Valid: true, String: "00000000-0000-4000-8000-000000000099"},
		Lines: []store.EditLine{
			{StartLine: 7, EndLine: 7, ContentSHA: sha256HexStr([]byte("old ai content"))},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Same line number, but the committed content differs (human rewrote it).
	added := []AddedLine{{File: "foo.go", LineNum: 7, Content: "new human content"}}

	note, err := buildNote(db, repo, "newsha", now, added, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildNote: %v", err)
	}
	l := note.Files[0].Lines[0]
	if l.AuthorType != "Human" {
		t.Errorf("overridden line: AuthorType want Human, got %q", l.AuthorType)
	}
}

// TestBuildNote_CrossSessionDeletionIsAI is the deletion counterpart to
// TestBuildNote_CrossSessionContentShaIsHuman: an AI tool recorded REMOVING a
// line in an EARLIER work-session (different session_id), and the human commits
// that deletion in a LATER commit. Unlike added lines (where cross-session
// identical content is a human paste → Human), a cross-session AI removal is
// credited AI: the AI genuinely deleted that content, the human merely staged
// it later.
func TestBuildNote_CrossSessionDeletionIsAI(t *testing.T) {
	db := openTestDB(t)
	repo := "/r"
	now := time.Now().UnixNano()

	const lineText = "    legacyHelper();"
	// AI removal recorded in some OTHER session (id 99), long before this commit.
	_, err := db.InsertEdit(store.Edit{
		TimestampNanos: now - int64(8*time.Hour),
		RepoPath:       repo,
		FilePath:       "foo.go",
		Tool:           store.ToolGemini,
		Confidence:     store.ConfidenceHigh,
		GenType:        store.GenTypeChat,
		Model:          sqlNullString("gemini-3"),
		Branch:         "main",
		SessionID:      sql.NullString{Valid: true, String: "00000000-0000-4000-8000-000000000099"},
		RemovedLines:   []store.RemovedLineHash{{ContentSHA: sha256HexStr([]byte(lineText)), ContentSHANorm: sha256HexNormStr(lineText)}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The commit (current session, NULL since no git repo here) deletes that line.
	deleted := map[string][]DeletedLine{"foo.go": {{LineNum: 130, Content: lineText}}}
	note, err := buildNote(db, repo, "newsha", now, nil, deleted, nil, nil)
	if err != nil {
		t.Fatalf("buildNote: %v", err)
	}
	if note.Totals.AIDeletedLines != 1 {
		t.Errorf("ai_deleted_lines: want 1, got %d", note.Totals.AIDeletedLines)
	}
	var found bool
	for _, f := range note.Files {
		for _, r := range f.Lines {
			if r.Type == "delete" {
				found = true
				if r.AuthorType != "AI" || r.Tool != string(store.ToolGemini) {
					t.Errorf("cross-session deletion: got author=%s tool=%s, want AI/gemini", r.AuthorType, r.Tool)
				}
			}
		}
	}
	if !found {
		t.Fatal("no delete range emitted")
	}
}

// TestBuildNote_DuplicateContentDistributedAcrossEdits: when the SAME line
// content is recorded by two AI edits — a (narrowed) chat that wrote it twice and
// a later completion that wrote it once — the committed copies must be split by
// recorded count (2 chat + 1 completion), not all claimed by the newest edit.
// Regression for commit 0e80e5f in mix-test (chat lines mislabelled completion).
func TestBuildNote_DuplicateContentDistributedAcrossEdits(t *testing.T) {
	db := openTestDB(t)
	repo := "/r"
	now := time.Now().UnixNano()
	const line = "  <p>cimbom</p>"
	sha := sha256HexStr([]byte(line))

	// Chat edit (older, narrowed): recorded the content twice, at lines 5 and 6.
	if _, err := db.InsertEdit(store.Edit{
		TimestampNanos: now - int64(5*time.Minute),
		RepoPath:       repo, FilePath: "index.html",
		Tool: store.ToolCopilot, Confidence: store.ConfidenceHigh, GenType: store.GenTypeChat,
		RawMeta: sql.NullString{Valid: true, String: `{"narrowed":true}`},
		Lines: []store.EditLine{
			{StartLine: 5, EndLine: 5, ContentSHA: sha},
			{StartLine: 6, EndLine: 6, ContentSHA: sha},
		},
	}); err != nil {
		t.Fatal(err)
	}
	// Completion edit (newer, full): recorded the content once, at line 9.
	if _, err := db.InsertEdit(store.Edit{
		TimestampNanos: now - int64(1*time.Minute),
		RepoPath:       repo, FilePath: "index.html",
		Tool: store.ToolCopilot, Confidence: store.ConfidenceHigh, GenType: store.GenTypeCompletion,
		Lines: []store.EditLine{
			{StartLine: 9, EndLine: 9, ContentSHA: sha},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Commit adds the content at line 9 (exact-matches completion) and lines
	// 20,21 (drifted — match no recorded position, must go to chat by budget).
	added := []AddedLine{
		{File: "index.html", LineNum: 9, Content: line},
		{File: "index.html", LineNum: 20, Content: line},
		{File: "index.html", LineNum: 21, Content: line},
	}
	note, err := buildNote(db, repo, "newsha", now, added, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildNote: %v", err)
	}
	if note.ByGenType.Chat != 2 {
		t.Errorf("by_gen_type chat: want 2, got %d", note.ByGenType.Chat)
	}
	if note.ByGenType.Completion != 1 {
		t.Errorf("by_gen_type completion: want 1, got %d (newest edit wrongly claimed duplicates)", note.ByGenType.Completion)
	}
}
