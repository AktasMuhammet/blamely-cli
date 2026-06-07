package daemon

import (
	"testing"
	"time"

	"github.com/blamely/blamely/internal/store"
)

func seedFsEdit(t *testing.T, db *store.DB, repo, file string) {
	t.Helper()
	if _, err := db.InsertEdit(store.Edit{
		TimestampNanos: time.Now().UnixNano(),
		RepoPath:       repo,
		FilePath:       file,
		Tool:           store.ToolClaude,
		Confidence:     store.ConfidenceHigh,
		Lines:          []store.EditLine{{StartLine: 1, EndLine: 2}},
	}); err != nil {
		t.Fatalf("seedFsEdit: %v", err)
	}
}

func liveCount(t *testing.T, db *store.DB, repo, file string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM edits WHERE repo_path=? AND file_path=? AND deleted_at IS NULL`,
		repo, file).Scan(&n); err != nil {
		t.Fatalf("liveCount: %v", err)
	}
	return n
}

func TestApplyFsEvent_DeleteRestore(t *testing.T) {
	db := openTestDB(t)
	seedFsEdit(t, db, "/r", "a.go")

	if err := applyFsEvent(db, FsEventPayload{Kind: "delete", RepoPath: "/r", Path: "a.go"}); err != nil {
		t.Fatal(err)
	}
	if liveCount(t, db, "/r", "a.go") != 0 {
		t.Error("delete should hide the row from live")
	}
	if err := applyFsEvent(db, FsEventPayload{Kind: "create", RepoPath: "/r", Path: "a.go"}); err != nil {
		t.Fatal(err)
	}
	if liveCount(t, db, "/r", "a.go") != 1 {
		t.Error("create should restore the soft-deleted row")
	}
}

func TestApplyFsEvent_RenameAndCopy(t *testing.T) {
	db := openTestDB(t)
	seedFsEdit(t, db, "/r", "old.go")

	// Backslashes from a Windows editor must normalise to forward slashes.
	if err := applyFsEvent(db, FsEventPayload{Kind: "rename", RepoPath: "/r", OldPath: "old.go", NewPath: `sub\new.go`}); err != nil {
		t.Fatal(err)
	}
	if liveCount(t, db, "/r", "old.go") != 0 || liveCount(t, db, "/r", "sub/new.go") != 1 {
		t.Error("rename should move the row to the normalised new path")
	}

	if err := applyFsEvent(db, FsEventPayload{Kind: "copy", RepoPath: "/r", SrcPath: "sub/new.go", DstPath: "copy.go"}); err != nil {
		t.Fatal(err)
	}
	if liveCount(t, db, "/r", "sub/new.go") != 1 || liveCount(t, db, "/r", "copy.go") != 1 {
		t.Error("copy should duplicate attribution to the destination")
	}
}

func TestApplyFsEvent_Validation(t *testing.T) {
	db := openTestDB(t)
	cases := []FsEventPayload{
		{Kind: "delete", RepoPath: "/r"},                    // missing path
		{Kind: "rename", RepoPath: "/r", OldPath: "a.go"},  // missing new_path
		{Kind: "copy", RepoPath: "/r", SrcPath: "a.go"},    // missing dst_path
		{Kind: "bogus", RepoPath: "/r", Path: "a.go"},      // unknown kind
		{Kind: "delete", Path: "a.go"},                     // missing repo_path
	}
	for i, c := range cases {
		if err := applyFsEvent(db, c); err == nil {
			t.Errorf("case %d: expected error for %+v", i, c)
		}
	}
}
