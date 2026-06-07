package store

import (
	"testing"
	"time"
)

// liveCount returns how many non-soft-deleted edit rows exist for repo/file.
func liveCount(t *testing.T, db *DB, repo, file string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM edits WHERE repo_path=? AND file_path=? AND deleted_at IS NULL`,
		repo, file,
	).Scan(&n); err != nil {
		t.Fatalf("liveCount: %v", err)
	}
	return n
}

func seedEdit(t *testing.T, db *DB, repo, file string) int64 {
	t.Helper()
	id, err := db.InsertEdit(Edit{
		TimestampNanos: time.Now().UnixNano(),
		RepoPath:       repo,
		FilePath:       file,
		Tool:           ToolClaude,
		Confidence:     ConfidenceHigh,
		Lines:          []EditLine{{StartLine: 1, EndLine: 3, ContentSHA: "abc"}},
	})
	if err != nil {
		t.Fatalf("seedEdit: %v", err)
	}
	return id
}

func TestMarkFileDeleted_AndRestore(t *testing.T) {
	db := openTestDB(t)
	const repo, file = "/r", "a.go"
	seedEdit(t, db, repo, file)
	seedEdit(t, db, repo, file)

	n, err := db.MarkFileDeleted(repo, file, time.Now().UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("MarkFileDeleted: want 2 rows marked, got %d", n)
	}
	if c := liveCount(t, db, repo, file); c != 0 {
		t.Errorf("after delete: want 0 live rows, got %d", c)
	}

	// Re-marking is a no-op (rows already deleted).
	if n, _ := db.MarkFileDeleted(repo, file, time.Now().UnixNano()); n != 0 {
		t.Errorf("second MarkFileDeleted: want 0, got %d", n)
	}

	restored, err := db.RestoreFile(repo, file)
	if err != nil {
		t.Fatal(err)
	}
	if restored != 2 {
		t.Fatalf("RestoreFile: want 2 restored, got %d", restored)
	}
	if c := liveCount(t, db, repo, file); c != 2 {
		t.Errorf("after restore: want 2 live rows, got %d", c)
	}
}

func TestMoveFileAttribution(t *testing.T) {
	db := openTestDB(t)
	const repo, oldP, newP = "/r", "old.go", "new.go"
	seedEdit(t, db, repo, oldP)
	seedEdit(t, db, repo, oldP)

	n, err := db.MoveFileAttribution(repo, oldP, newP)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("MoveFileAttribution: want 2 moved, got %d", n)
	}
	if c := liveCount(t, db, repo, oldP); c != 0 {
		t.Errorf("old path should have 0 rows, got %d", c)
	}
	if c := liveCount(t, db, repo, newP); c != 2 {
		t.Errorf("new path should have 2 rows, got %d", c)
	}
}

func TestMoveFileAttribution_ClearsSoftDelete(t *testing.T) {
	db := openTestDB(t)
	const repo, oldP, newP = "/r", "old.go", "new.go"
	seedEdit(t, db, repo, oldP)
	if _, err := db.MarkFileDeleted(repo, oldP, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	// A move of a soft-deleted source (e.g. rename of a restored file) makes the
	// destination live again.
	if _, err := db.MoveFileAttribution(repo, oldP, newP); err != nil {
		t.Fatal(err)
	}
	if c := liveCount(t, db, repo, newP); c != 1 {
		t.Errorf("moved row should be live at new path, got %d", c)
	}
}

func TestMoveFileAttribution_SamePathNoop(t *testing.T) {
	db := openTestDB(t)
	seedEdit(t, db, "/r", "f.go")
	if n, err := db.MoveFileAttribution("/r", "f.go", "f.go"); err != nil || n != 0 {
		t.Errorf("same-path move: want (0,nil), got (%d,%v)", n, err)
	}
}

func TestCopyFileAttribution(t *testing.T) {
	db := openTestDB(t)
	const repo, src, dst = "/r", "src.go", "dst.go"
	seedEdit(t, db, repo, src)
	seedEdit(t, db, repo, src)

	n, err := db.CopyFileAttribution(repo, src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("CopyFileAttribution: want 2 cloned, got %d", n)
	}
	// Source untouched, destination has the clones.
	if c := liveCount(t, db, repo, src); c != 2 {
		t.Errorf("source should still have 2 rows, got %d", c)
	}
	if c := liveCount(t, db, repo, dst); c != 2 {
		t.Errorf("dest should have 2 cloned rows, got %d", c)
	}

	// Clones carry the line ranges.
	edits, err := db.EditsForFileSince(repo, dst, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) == 0 || len(edits[0].Lines) != 1 || edits[0].Lines[0].ContentSHA != "abc" {
		t.Errorf("clone did not carry edit_lines: %+v", edits)
	}
}

func TestCopyFileAttribution_Idempotent(t *testing.T) {
	db := openTestDB(t)
	const repo, src, dst = "/r", "src.go", "dst.go"
	seedEdit(t, db, repo, src)

	if _, err := db.CopyFileAttribution(repo, src, dst); err != nil {
		t.Fatal(err)
	}
	// Re-copy must not stack duplicate rows at the destination.
	if _, err := db.CopyFileAttribution(repo, src, dst); err != nil {
		t.Fatal(err)
	}
	if c := liveCount(t, db, repo, dst); c != 1 {
		t.Errorf("re-copy should be idempotent (1 row), got %d", c)
	}
}

func TestCopyFileAttribution_SkipsSoftDeleted(t *testing.T) {
	db := openTestDB(t)
	const repo, src, dst = "/r", "src.go", "dst.go"
	seedEdit(t, db, repo, src)
	if _, err := db.MarkFileDeleted(repo, src, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	n, err := db.CopyFileAttribution(repo, src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("copy of a soft-deleted source should clone 0 rows, got %d", n)
	}
}
