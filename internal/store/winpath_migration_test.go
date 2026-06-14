package store

import "testing"

// Edits recorded by old Windows clients stored nested file paths with
// backslashes (filepath.Rel uses the OS separator), which never matched git
// diff's forward-slash paths at commit time. Migrations 21/22 backfill them so
// existing installs self-heal. We simulate a pre-migration DB by rewinding
// schema_version, then re-run migrate().
func TestMigration_BackfillsWindowsBackslashPaths(t *testing.T) {
	db := openTestDB(t)

	// Rows as an old Windows daemon would have written them.
	if _, err := db.InsertEdit(Edit{
		TimestampNanos: 1,
		RepoPath:       "/r",
		FilePath:       `src\app\main.go`,
		Tool:           ToolCodex,
		Confidence:     ConfidenceHigh,
		GenType:        GenTypeCLI,
		Lines:          []EditLine{{StartLine: 1, EndLine: 1, ContentSHA: "abc"}},
	}); err != nil {
		t.Fatalf("InsertEdit: %v", err)
	}
	if err := db.SetFileSnapshot("/r", `src\app\main.go`, "x", 1); err != nil {
		t.Fatalf("SetFileSnapshot: %v", err)
	}

	// A Unix-style row must be left untouched by the backfill.
	if _, err := db.InsertEdit(Edit{
		TimestampNanos: 2,
		RepoPath:       "/r",
		FilePath:       "src/app/other.go",
		Tool:           ToolClaude,
		Confidence:     ConfidenceHigh,
		GenType:        GenTypeChat,
		Lines:          []EditLine{{StartLine: 1, EndLine: 1, ContentSHA: "def"}},
	}); err != nil {
		t.Fatalf("InsertEdit unix: %v", err)
	}

	// Rewind so the backfill migrations (indices 21, 22) re-run.
	if _, err := db.Exec(`DELETE FROM schema_version`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_version(version) VALUES (21)`); err != nil {
		t.Fatal(err)
	}
	if err := db.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// The backslash edit is now found under the forward-slash path git uses.
	edits, err := db.EditsForFileSince("/r", "src/app/main.go", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 1 || edits[0].FilePath != "src/app/main.go" {
		t.Fatalf("backslash edit not normalized: %+v", edits)
	}
	// And nothing remains under the old backslash key.
	if old, _ := db.EditsForFileSince("/r", `src\app\main.go`, 0); len(old) != 0 {
		t.Errorf("backslash row still present: %+v", old)
	}
	// The Unix row is untouched.
	unix, err := db.EditsForFileSince("/r", "src/app/other.go", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(unix) != 1 {
		t.Errorf("unix row was disturbed: %+v", unix)
	}
	// The stale snapshot was dropped (it regenerates on the next edit).
	if _, ok, _ := db.GetFileSnapshot("/r", `src\app\main.go`); ok {
		t.Errorf("stale backslash snapshot was not dropped")
	}
}
