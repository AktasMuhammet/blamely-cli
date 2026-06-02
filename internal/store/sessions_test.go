package store

import (
	"database/sql"
	"testing"

	"github.com/google/uuid"
)

func TestResolveSession_Idempotent(t *testing.T) {
	db := openTestDB(t)

	id1, err := db.ResolveSession("/r", "feature/x", "base123")
	if err != nil {
		t.Fatal(err)
	}
	if id1 == "" || uuid.Validate(id1) != nil {
		t.Fatalf("expected UUID session id, got %q", id1)
	}
	id2, err := db.ResolveSession("/r", "feature/x", "base123")
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("expected idempotent resolve, got %q and %q", id1, id2)
	}
	id3, err := db.ResolveSession("/r", "feature/x", "base999")
	if err != nil {
		t.Fatal(err)
	}
	if id3 == id1 {
		t.Fatalf("expected distinct session for different base_sha")
	}
	a, _ := db.ResolveSession("/r", "main", "")
	b, _ := db.ResolveSession("/r", "main", "")
	if a != b {
		t.Fatalf("expected empty base_sha to dedupe, got %q and %q", a, b)
	}
}

func TestEditsForFileInSession_ScopesAndIncludesNull(t *testing.T) {
	db := openTestDB(t)
	sid, _ := db.ResolveSession("/r", "feature/x", "base")
	other, _ := db.ResolveSession("/r", "feature/y", "base")

	mk := func(session sql.NullString, line int) {
		e := Edit{
			RepoPath: "/r", FilePath: "f.go", Tool: ToolClaude,
			Confidence: ConfidenceHigh, GenType: GenTypeChat,
			SessionID: session,
			Lines:     []EditLine{{StartLine: line, EndLine: line}},
		}
		if _, err := db.InsertEdit(e); err != nil {
			t.Fatal(err)
		}
	}
	mk(sql.NullString{Valid: true, String: sid}, 1)
	mk(sql.NullString{Valid: true, String: other}, 2)
	mk(sql.NullString{}, 3)

	got, err := db.EditsForFileInSession("/r", "f.go", sid)
	if err != nil {
		t.Fatal(err)
	}
	lines := map[int]bool{}
	for _, e := range got {
		for _, l := range e.Lines {
			lines[l.StartLine] = true
		}
	}
	if !lines[1] || !lines[3] {
		t.Errorf("expected lines 1 and 3 in session query, got %v", lines)
	}
	if lines[2] {
		t.Errorf("did not expect other session's line 2, got %v", lines)
	}
}

func TestMigrateWorkSessionsUUID_Idempotent(t *testing.T) {
	db := openTestDB(t)
	if !sessionsIDIsText(db) {
		t.Fatal("openTestDB should run migration 15 to TEXT session ids")
	}
	if err := db.migrateWorkSessionsUUID(); err != nil {
		t.Fatal(err)
	}
}
