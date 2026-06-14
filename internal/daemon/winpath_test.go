package daemon

import (
	"testing"

	"github.com/blamely/blamely/internal/store"
)

// On Windows the Go recorders (codex, claude) build file_path with filepath.Rel,
// which uses backslashes for nested files. git diff — the source of truth at
// commit time — always uses forward slashes, so a backslash row never matched
// and the AI line fell back to Human. Both ingestion paths must normalize.

func TestValidateAndStore_NormalizesWindowsBackslashPath(t *testing.T) {
	db := openTestDB(t)
	p := minimalPayload()
	p.Tool = "codex"
	p.FilePath = `src\app\main.go` // as a Windows recorder would send it
	if err := validateAndStore(db, p); err != nil {
		t.Fatalf("validateAndStore: %v", err)
	}
	// The committed note looks the file up by git's forward-slash path.
	edits, err := db.EditsForFileSince("/repo", "src/app/main.go", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit under forward-slash path, got %d", len(edits))
	}
	if edits[0].FilePath != "src/app/main.go" {
		t.Errorf("stored file_path = %q, want src/app/main.go", edits[0].FilePath)
	}
}

func TestDbSinkRecord_NormalizesWindowsBackslashPath(t *testing.T) {
	db := openTestDB(t)
	sink := &dbSink{db: db}
	if err := sink.Record(Event{
		Tool:     "codex",
		GenType:  string(store.GenTypeCLI),
		RepoPath: "/repo",
		FilePath: `pkg\util\fs.go`,
		Lines:    []LineRange{{Start: 1, End: 2}},
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	edits, err := db.EditsForFileSince("/repo", "pkg/util/fs.go", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit under forward-slash path, got %d", len(edits))
	}
	if edits[0].FilePath != "pkg/util/fs.go" {
		t.Errorf("stored file_path = %q, want pkg/util/fs.go", edits[0].FilePath)
	}
}

// A plain Unix path with no backslashes must pass through byte-for-byte — the
// fix is a no-op off Windows.
func TestValidateAndStore_ForwardSlashPathUnchanged(t *testing.T) {
	db := openTestDB(t)
	p := minimalPayload()
	p.FilePath = "src/app/main.go"
	if err := validateAndStore(db, p); err != nil {
		t.Fatalf("validateAndStore: %v", err)
	}
	edits, err := db.EditsForFileSince("/repo", "src/app/main.go", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 1 || edits[0].FilePath != "src/app/main.go" {
		t.Fatalf("forward-slash path was altered: %+v", edits)
	}
}
