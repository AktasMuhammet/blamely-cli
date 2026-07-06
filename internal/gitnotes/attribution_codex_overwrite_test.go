package gitnotes

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/blamely/blamely/internal/store"
)

// TestReconcileDeletes_CodexOverwriteAdd is the note-level end-to-end for the
// field bug "Codex CLI deletions show as Human". Codex reports a full-file
// OVERWRITE as an "add" (raw_meta patch_apply:add), and the codex watcher now
// fingerprints the overwritten HEAD lines as RemovedLines. This asserts those
// removed hashes reattribute the committed deletions to codex/cli — i.e. the
// edit is NOT excluded by editFromWholeFileWrite (that guard only fires for the
// editor "write"/"create_file" tools, whose per-line content is unreliable; a
// codex apply_patch carries the AI's real diff/content).
func TestReconcileDeletes_CodexOverwriteAdd(t *testing.T) {
	db, err := store.OpenAt(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer db.Close()

	const repo, file = "/repo", "employ_register.html"
	// The old committed lines Codex overwrote (its "-N" at commit time).
	gone := []string{"<html>", "<head><title>Old</title></head>", "<body>", "<form><input name=\"x\"></form>", "</body>", "</html>"}
	if _, err := db.InsertEdit(store.Edit{
		TimestampNanos: 1000, RepoPath: repo, FilePath: file,
		Tool: "codex", Confidence: "high", GenType: "cli",
		Model:        sql.NullString{String: "gpt-5-codex", Valid: true},
		RemovedLines: removedLines(gone...),
		// The exact meta the codex watcher writes for a patch_apply "add".
		RawMeta: sql.NullString{String: `{"source":"codex_session","patch_apply":"add"}`, Valid: true},
	}); err != nil {
		t.Fatalf("InsertEdit: %v", err)
	}

	// What the deletion flip produced before reconciliation: all 6 lines Human.
	fe := &FileEntry{Path: file}
	delRange(fe, 1, 6)
	note := &Note{Schema: 2, Files: []FileEntry{*fe}}
	note.Totals.DeletedLines = 6
	note.Totals.HumanDeletedLines = 6

	change := &CommitChange{
		Deleted: map[string][]DeletedLine{file: {
			{LineNum: 1, Content: gone[0]}, {LineNum: 2, Content: gone[1]}, {LineNum: 3, Content: gone[2]},
			{LineNum: 4, Content: gone[3]}, {LineNum: 5, Content: gone[4]}, {LineNum: 6, Content: gone[5]},
		}},
		Renames: map[string]string{},
	}

	reconcileDeletesFromEdits(db, repo, 2000, note, change)

	for ln := 1; ln <= 6; ln++ {
		r := delRangeAuthor(t, note, file, ln)
		if r.AuthorType != "AI" || r.Tool != "codex" || ptrStr(r.GenType) != "cli" {
			t.Errorf("deleted line %d: want AI/codex/cli, got author=%q tool=%q gen=%q",
				ln, r.AuthorType, r.Tool, ptrStr(r.GenType))
		}
	}
	if note.Totals.AIDeletedLines != 6 || note.Totals.HumanDeletedLines != 0 {
		t.Errorf("totals: want AIdel=6 humandel=0, got AIdel=%d humandel=%d",
			note.Totals.AIDeletedLines, note.Totals.HumanDeletedLines)
	}
}
