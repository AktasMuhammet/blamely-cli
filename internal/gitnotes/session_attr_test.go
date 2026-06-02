package gitnotes

import (
	"database/sql"
	"testing"
	"time"

	"github.com/blamely/blamely/internal/store"
)

// TestBuildNote_CherryPickContentShaFallback verifies that an AI edit recorded
// under a DIFFERENT work session (e.g. on the source branch) is still attributed
// to AI when its content lands in a commit via cherry-pick/squash. The new commit
// has a fresh SHA/timestamp and resolves to a different (or no) session, so the
// session-scoped primary query misses the edit — but the repo-wide content_sha
// fallback re-attributes it because the line content (and thus its hash) is
// unchanged.
func TestBuildNote_CherryPickContentShaFallback(t *testing.T) {
	db := openTestDB(t)
	repo := "/r"
	now := time.Now().UnixNano()

	const lineText = "    return doWork()"
	// AI edit recorded long ago, in some other session (id 99), carrying the
	// content_sha for the line. buildNote here resolves session 0 (no git repo),
	// so this edit is NOT in the primary (session 0 / NULL) set.
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

	// The cherry-picked commit adds the SAME content at a DIFFERENT line number.
	added := []AddedLine{{File: "foo.go", LineNum: 7, Content: lineText}}

	note, err := buildNote(db, repo, "newsha", now, added, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildNote: %v", err)
	}
	if len(note.Files) != 1 || len(note.Files[0].Lines) != 1 {
		t.Fatalf("expected 1 file/1 line, got %+v", note.Files)
	}
	l := note.Files[0].Lines[0]
	if l.AuthorType != "AI" {
		t.Errorf("cherry-picked AI line: AuthorType want AI, got %q", l.AuthorType)
	}
	if l.Tool != "claude" {
		t.Errorf("cherry-picked AI line: Tool want claude, got %q", l.Tool)
	}
	if note.Totals.AILines != 1 {
		t.Errorf("ai_lines: want 1, got %d", note.Totals.AILines)
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
