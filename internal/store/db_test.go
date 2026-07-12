package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := OpenAt(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpenAt_MigratesSchema(t *testing.T) {
	db := openTestDB(t)
	// Verify all three tables exist.
	for _, table := range []string{"edits", "edit_lines", "commits"} {
		row := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table)
		var name string
		if err := row.Scan(&name); err != nil {
			t.Errorf("table %q not found after migration: %v", table, err)
		}
	}
}

func TestOpenAt_Idempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idempotent.sqlite")
	db1, err := OpenAt(path)
	if err != nil {
		t.Fatal(err)
	}
	db1.Close()
	// Second open of the same file should succeed.
	db2, err := OpenAt(path)
	if err != nil {
		t.Fatalf("second OpenAt: %v", err)
	}
	db2.Close()
}

func TestInsertEdit_NoLines(t *testing.T) {
	db := openTestDB(t)
	e := Edit{
		TimestampNanos: time.Now().UnixNano(),
		RepoPath:       "/repo",
		FilePath:       "main.go",
		Tool:           ToolClaude,
		Confidence:     ConfidenceHigh,
		Model:          sql.NullString{Valid: true, String: "claude-opus-4-7"},
		InputTokens:    sql.NullInt64{Valid: true, Int64: 1200},
		OutputTokens:   sql.NullInt64{Valid: true, Int64: 340},
	}
	id, err := db.InsertEdit(e)
	if err != nil {
		t.Fatalf("InsertEdit: %v", err)
	}
	if id <= 0 {
		t.Errorf("expected positive id, got %d", id)
	}
}

func TestInsertEdit_WithLines(t *testing.T) {
	db := openTestDB(t)
	e := Edit{
		TimestampNanos: time.Now().UnixNano(),
		RepoPath:       "/repo",
		FilePath:       "pkg/x.go",
		Tool:           ToolCursor,
		Confidence:     ConfidenceMedium,
		Lines: []EditLine{
			{StartLine: 1, EndLine: 5, ContentSHA: "sha1"},
			{StartLine: 10, EndLine: 12, ContentSHA: "sha2"},
		},
	}
	id, err := db.InsertEdit(e)
	if err != nil {
		t.Fatalf("InsertEdit with lines: %v", err)
	}

	lines, err := db.linesForEdit(id)
	if err != nil {
		t.Fatalf("linesForEdit: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d", len(lines))
	}
	if lines[0].StartLine != 1 || lines[0].EndLine != 5 {
		t.Errorf("first line: want [1,5], got [%d,%d]", lines[0].StartLine, lines[0].EndLine)
	}
	if lines[1].ContentSHA != "sha2" {
		t.Errorf("second line SHA: want sha2, got %s", lines[1].ContentSHA)
	}
}

func TestEditsForFileSince_ReturnsNewestFirst(t *testing.T) {
	db := openTestDB(t)
	now := time.Now().UnixNano()
	for _, ts := range []int64{now - 1000, now - 500, now} {
		_, err := db.InsertEdit(Edit{
			TimestampNanos: ts,
			RepoPath:       "/r",
			FilePath:       "f.go",
			Tool:           ToolClaude,
			Confidence:     ConfidenceHigh,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	edits, err := db.EditsForFileSince("/r", "f.go", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 3 {
		t.Fatalf("want 3 edits, got %d", len(edits))
	}
	// newest-first ordering
	if edits[0].TimestampNanos < edits[1].TimestampNanos {
		t.Errorf("edits should be newest-first")
	}
}

func TestEditsForFileSince_FiltersByPath(t *testing.T) {
	db := openTestDB(t)
	ts := time.Now().UnixNano()
	for _, file := range []string{"a.go", "b.go", "a.go"} {
		_, err := db.InsertEdit(Edit{
			TimestampNanos: ts,
			RepoPath:       "/r",
			FilePath:       file,
			Tool:           ToolClaude,
			Confidence:     ConfidenceHigh,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	edits, err := db.EditsForFileSince("/r", "a.go", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 2 {
		t.Errorf("want 2 edits for a.go, got %d", len(edits))
	}
}

func TestEditsForFileSince_FiltersByRepo(t *testing.T) {
	db := openTestDB(t)
	ts := time.Now().UnixNano()
	for _, repo := range []string{"/r1", "/r2", "/r1"} {
		_, err := db.InsertEdit(Edit{
			TimestampNanos: ts,
			RepoPath:       repo,
			FilePath:       "f.go",
			Tool:           ToolClaude,
			Confidence:     ConfidenceHigh,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	edits, err := db.EditsForFileSince("/r1", "f.go", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 2 {
		t.Errorf("want 2 edits for /r1, got %d", len(edits))
	}
}

func TestEditsForFileSince_SinceFilter(t *testing.T) {
	db := openTestDB(t)
	base := int64(1_000_000_000)
	for _, ts := range []int64{base, base + 100, base + 200} {
		_, err := db.InsertEdit(Edit{
			TimestampNanos: ts,
			RepoPath:       "/r",
			FilePath:       "f.go",
			Tool:           ToolClaude,
			Confidence:     ConfidenceHigh,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// since=base+50 should return only the two edits at +100 and +200
	edits, err := db.EditsForFileSince("/r", "f.go", base+50)
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 2 {
		t.Errorf("want 2 edits after sinceNanos, got %d", len(edits))
	}
}

func TestEditsForFileSince_AttachesLines(t *testing.T) {
	db := openTestDB(t)
	id, err := db.InsertEdit(Edit{
		TimestampNanos: time.Now().UnixNano(),
		RepoPath:       "/r",
		FilePath:       "x.go",
		Tool:           ToolCodex,
		Confidence:     ConfidenceHigh,
		Lines: []EditLine{
			{StartLine: 3, EndLine: 7, ContentSHA: "abc"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = id
	edits, err := db.EditsForFileSince("/r", "x.go", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 1 {
		t.Fatal("expected 1 edit")
	}
	if len(edits[0].Lines) != 1 {
		t.Fatalf("expected 1 edit_line attached, got %d", len(edits[0].Lines))
	}
	if edits[0].Lines[0].StartLine != 3 || edits[0].Lines[0].EndLine != 7 {
		t.Errorf("line range: want [3,7], got [%d,%d]", edits[0].Lines[0].StartLine, edits[0].Lines[0].EndLine)
	}
}

func TestMarkCommitNoted(t *testing.T) {
	db := openTestDB(t)
	sha := "abc123def456"
	if err := db.MarkCommitNoted(sha, "/repo", time.Now().UnixNano()); err != nil {
		t.Fatalf("MarkCommitNoted: %v", err)
	}
	// Second call should also succeed (UPSERT).
	if err := db.MarkCommitNoted(sha, "/repo", time.Now().UnixNano()); err != nil {
		t.Fatalf("MarkCommitNoted (upsert): %v", err)
	}
	var noteWritten int
	row := db.QueryRow("SELECT note_written FROM commits WHERE sha=?", sha)
	if err := row.Scan(&noteWritten); err != nil {
		t.Fatalf("query commit: %v", err)
	}
	if noteWritten != 1 {
		t.Errorf("note_written: want 1, got %d", noteWritten)
	}
}

// ---- SessionDurationNanos ----

func TestSessionDurationNanos_FirstCommit_UsesEarliestEditInWindow(t *testing.T) {
	db := openTestDB(t)
	const repo = "/repo"
	commitTS := int64(10_000_000_000_000) // arbitrary base

	// Edit 1 hour before the commit. No prior commits exist, so the lookup
	// window is bounded by the 8h cap on the lower side.
	editTS := commitTS - int64(time.Hour)
	if _, err := db.InsertEdit(Edit{
		TimestampNanos: editTS,
		RepoPath:       repo,
		FilePath:       "foo.go",
		Tool:           ToolHuman,
		Confidence:     ConfidenceHigh,
	}); err != nil {
		t.Fatalf("InsertEdit: %v", err)
	}

	got := db.SessionDurationNanos(repo, commitTS)
	if want := int64(time.Hour); got != want {
		t.Errorf("session duration: want %v, got %v", time.Duration(want), time.Duration(got))
	}
}

func TestSessionDurationNanos_ResetsAfterPreviousCommit(t *testing.T) {
	// Regression test for the per-commit reset bug. If commit A is at T,
	// then commit B at T+15min, then commit B's session duration must be
	// computed from edits AFTER A — not from the earliest edit in the
	// last 8 hours.
	db := openTestDB(t)
	const repo = "/repo"
	prevCommitTS := int64(10_000_000_000_000)
	currCommitTS := prevCommitTS + int64(15*time.Minute)

	// Mark the previous commit at T.
	if err := db.MarkCommitNoted("prev_sha", repo, prevCommitTS); err != nil {
		t.Fatalf("MarkCommitNoted: %v", err)
	}

	// Edit from BEFORE the previous commit (2 hours back). Must NOT
	// contribute to the current commit's session.
	if _, err := db.InsertEdit(Edit{
		TimestampNanos: prevCommitTS - int64(2*time.Hour),
		RepoPath:       repo,
		FilePath:       "old.go",
		Tool:           ToolHuman,
	}); err != nil {
		t.Fatalf("InsertEdit old: %v", err)
	}

	// Edit 10 minutes before the current commit (so 5 min after the
	// previous commit). This is the earliest qualifying edit and should
	// produce a 10-minute coding time.
	if _, err := db.InsertEdit(Edit{
		TimestampNanos: currCommitTS - int64(10*time.Minute),
		RepoPath:       repo,
		FilePath:       "new.go",
		Tool:           ToolHuman,
	}); err != nil {
		t.Fatalf("InsertEdit new: %v", err)
	}

	got := db.SessionDurationNanos(repo, currCommitTS)
	if want := int64(10 * time.Minute); got != want {
		t.Errorf("session duration: want %v (10 min), got %v", time.Duration(want), time.Duration(got))
	}
}

func TestSessionDurationNanos_NoEditsInWindow(t *testing.T) {
	db := openTestDB(t)
	const repo = "/repo"
	commitTS := int64(10_000_000_000_000)

	// Mark a prior commit. No edits between it and the new commit.
	if err := db.MarkCommitNoted("prev", repo, commitTS-int64(time.Hour)); err != nil {
		t.Fatalf("MarkCommitNoted: %v", err)
	}
	// Edit that predates the prior commit (must be ignored).
	if _, err := db.InsertEdit(Edit{
		TimestampNanos: commitTS - int64(2*time.Hour),
		RepoPath:       repo,
		FilePath:       "x.go",
		Tool:           ToolHuman,
	}); err != nil {
		t.Fatalf("InsertEdit: %v", err)
	}

	if got := db.SessionDurationNanos(repo, commitTS); got != 0 {
		t.Errorf("session duration: want 0 (no edits in window), got %v", time.Duration(got))
	}
}

func TestSessionDurationNanos_OnlyCountsOwnRepo(t *testing.T) {
	db := openTestDB(t)
	commitTS := int64(10_000_000_000_000)

	// Edit in a DIFFERENT repo, near the commit time. Must not contribute.
	if _, err := db.InsertEdit(Edit{
		TimestampNanos: commitTS - int64(20*time.Minute),
		RepoPath:       "/other-repo",
		FilePath:       "a.go",
		Tool:           ToolHuman,
	}); err != nil {
		t.Fatalf("InsertEdit other: %v", err)
	}
	// Edit in the target repo, 5 min before commit.
	if _, err := db.InsertEdit(Edit{
		TimestampNanos: commitTS - int64(5*time.Minute),
		RepoPath:       "/repo",
		FilePath:       "b.go",
		Tool:           ToolHuman,
	}); err != nil {
		t.Fatalf("InsertEdit target: %v", err)
	}

	got := db.SessionDurationNanos("/repo", commitTS)
	if want := int64(5 * time.Minute); got != want {
		t.Errorf("session duration: want 5 min, got %v", time.Duration(got))
	}
}

func TestPreviousCommitTimestampNanos_NoPrior(t *testing.T) {
	db := openTestDB(t)
	// No commits in the DB → should return 0 (include all AI records).
	got := db.PreviousCommitTimestampNanos("/repo/a", int64(1_000_000_000_000))
	if got != 0 {
		t.Errorf("want 0 when no prior commits, got %d", got)
	}
}

func TestPreviousCommitTimestampNanos_ReturnsMostRecent(t *testing.T) {
	db := openTestDB(t)
	repo := "/repo/a"
	now := int64(1_000_000_000_000)
	// Two prior commits at T-300 and T-100; the function must return T-100.
	_ = db.MarkCommitNoted("sha1", repo, now-300)
	_ = db.MarkCommitNoted("sha2", repo, now-100)
	got := db.PreviousCommitTimestampNanos(repo, now)
	if got != now-100 {
		t.Errorf("want %d (T-100), got %d", now-100, got)
	}
}

func TestPreviousCommitTimestampNanos_ExcludesCurrentCommit(t *testing.T) {
	db := openTestDB(t)
	repo := "/repo/a"
	now := int64(1_000_000_000_000)
	// A commit at exactly `now` must NOT be returned (strict ts < beforeNanos).
	_ = db.MarkCommitNoted("sha1", repo, now)
	_ = db.MarkCommitNoted("sha2", repo, now-50)
	got := db.PreviousCommitTimestampNanos(repo, now)
	if got != now-50 {
		t.Errorf("want %d (T-50, not T=now), got %d", now-50, got)
	}
}

func TestPreviousCommitTimestampNanos_WrongRepo(t *testing.T) {
	db := openTestDB(t)
	now := int64(1_000_000_000_000)
	// A commit in a different repo must not affect the result.
	_ = db.MarkCommitNoted("sha1", "/repo/other", now-100)
	got := db.PreviousCommitTimestampNanos("/repo/a", now)
	if got != 0 {
		t.Errorf("want 0 (no prior commit for /repo/a), got %d", got)
	}
}

func TestInsertEdit_OnDeleteCascade(t *testing.T) {
	db := openTestDB(t)
	id, _ := db.InsertEdit(Edit{
		TimestampNanos: time.Now().UnixNano(),
		RepoPath:       "/r", FilePath: "f.go",
		Tool: ToolClaude, Confidence: ConfidenceHigh,
		Lines: []EditLine{{StartLine: 1, EndLine: 3}},
	})
	// Delete the parent edit row directly.
	if _, err := db.Exec("DELETE FROM edits WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	// edit_lines rows should be gone (foreign key cascade).
	row := db.QueryRow("SELECT COUNT(*) FROM edit_lines WHERE edit_id=?", id)
	var count int
	if err := row.Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("expected cascade delete of edit_lines, found %d rows", count)
	}
}

func TestEditAcceptedLines(t *testing.T) {
	cases := []struct {
		name  string
		lines []EditLine
		want  int64
	}{
		{"empty", nil, 0},
		{"single line", []EditLine{{StartLine: 5, EndLine: 5}}, 1},
		{"five-line range", []EditLine{{StartLine: 1, EndLine: 5}}, 5},
		{"two ranges", []EditLine{{StartLine: 1, EndLine: 3}, {StartLine: 7, EndLine: 9}}, 6},
		{"reversed range ignored", []EditLine{{StartLine: 5, EndLine: 3}}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := Edit{Lines: c.lines}
			if got := e.AcceptedLines(); got != c.want {
				t.Errorf("AcceptedLines: want %d, got %d", c.want, got)
			}
		})
	}
}

func TestInsertEdit_SuggestedLinesRoundTrip(t *testing.T) {
	db := openTestDB(t)
	id, err := db.InsertEdit(Edit{
		TimestampNanos: time.Now().UnixNano(),
		RepoPath:       "/r", FilePath: "f.go",
		Tool: ToolClaude, Confidence: ConfidenceHigh,
		SuggestedLines: 10,
		Lines:          []EditLine{{StartLine: 1, EndLine: 6}},
	})
	if err != nil {
		t.Fatalf("InsertEdit: %v", err)
	}

	edits, err := db.EditsForFileSince("/r", "f.go", 0)
	if err != nil {
		t.Fatalf("EditsForFileSince: %v", err)
	}
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	e := edits[0]
	if e.ID != id {
		t.Errorf("id: want %d, got %d", id, e.ID)
	}
	if e.SuggestedLines != 10 {
		t.Errorf("SuggestedLines: want 10, got %d", e.SuggestedLines)
	}
	if got := e.AcceptedLines(); got != 6 {
		t.Errorf("AcceptedLines: want 6 (lines 1..6), got %d", got)
	}
}
