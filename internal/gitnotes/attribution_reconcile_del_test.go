package gitnotes

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/blamely/blamely/internal/store"
)

// removedLines builds the edit_removed_lines records a chat watcher writes for
// deleted content: a content_sha/content_sha_norm per non-blank removed line (no
// positions — deleted lines have no stable post-edit location).
func removedLines(texts ...string) []store.RemovedLineHash {
	out := make([]store.RemovedLineHash, 0, len(texts))
	for _, s := range texts {
		out = append(out, store.RemovedLineHash{
			ContentSHA: sha256HexStr([]byte(s)), ContentSHANorm: sha256HexNormStr(s),
		})
	}
	return out
}

func delRange(fe *FileEntry, start, end int) {
	fe.Lines = append(fe.Lines, RangeEntry{Start: start, End: end, Type: "delete", AuthorType: "Human"})
}

func delRangeAuthor(t *testing.T, note *Note, path string, ln int) RangeEntry {
	t.Helper()
	for fi := range note.Files {
		if note.Files[fi].Path != path {
			continue
		}
		for _, r := range note.Files[fi].Lines {
			if r.Type == "delete" && r.Start <= ln && ln <= r.End {
				return r
			}
		}
	}
	t.Fatalf("no delete range covering %s:%d", path, ln)
	return RangeEntry{}
}

// TestReconcileDeletesFromEdits reproduces commit 682c5689: copilot chat deletes a
// block, recording removed-line hashes in SQLite, but the deletions log is empty so
// the flip leaves them Human. The SQLite removed hashes reattribute them to copilot.
func TestReconcileDeletesFromEdits(t *testing.T) {
	db, err := store.OpenAt(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer db.Close()

	const repo, file = "/repo", "login.html"
	gone := []string{
		`  <a href="/auth/github" class="github-btn">`,
		`    Sign in with GitHub`,
		`  </a>`,
	}
	if _, err := db.InsertEdit(store.Edit{
		TimestampNanos: 1000, RepoPath: repo, FilePath: file,
		Tool: "copilot", Confidence: "high", GenType: "chat",
		Model:        sql.NullString{String: "gpt-5-mini", Valid: true},
		RemovedLines: removedLines(gone...),
	}); err != nil {
		t.Fatalf("InsertEdit: %v", err)
	}

	// What the deletion flip produced (no deletions-log entry): all Human.
	fe := &FileEntry{Path: file}
	delRange(fe, 195, 197)
	note := &Note{Schema: 2, Files: []FileEntry{*fe}}
	note.Totals.DeletedLines = 3
	note.Totals.HumanDeletedLines = 3

	change := &CommitChange{
		Deleted: map[string][]DeletedLine{
			file: {
				{LineNum: 195, Content: gone[0]},
				{LineNum: 196, Content: gone[1]},
				{LineNum: 197, Content: gone[2]},
			},
		},
		Renames: map[string]string{},
	}

	reconcileDeletesFromEdits(db, repo, 2000, note, change)

	for ln := 195; ln <= 197; ln++ {
		r := delRangeAuthor(t, note, file, ln)
		if r.AuthorType != "AI" || r.Tool != "copilot" || ptrStr(r.GenType) != "chat" {
			t.Errorf("deleted line %d: want AI/copilot/chat, got author=%q tool=%q gen=%q",
				ln, r.AuthorType, r.Tool, ptrStr(r.GenType))
		}
	}
	if note.Totals.AIDeletedLines != 3 || note.Totals.HumanDeletedLines != 0 {
		t.Errorf("totals: want AIdel=3 humandel=0, got AIdel=%d humandel=%d",
			note.Totals.AIDeletedLines, note.Totals.HumanDeletedLines)
	}
	if note.ByTool["copilot"].DeletedLines != 3 {
		t.Errorf("by_tool copilot deleted: want 3, got %d", note.ByTool["copilot"].DeletedLines)
	}
}

// TestRecomputeByGenType verifies by_gen_type is rebuilt from ALL ranges — added and
// deleted, AI by gen_type and human as human — and is idempotent (any prior value is
// discarded), so it can't double-count deletions on a pure-deletion commit.
func TestRecomputeByGenType(t *testing.T) {
	gt := "chat"
	note := &Note{Schema: 2, Files: []FileEntry{{Path: "f", Lines: []RangeEntry{
		{Start: 1, End: 1, Type: "add", AuthorType: "AI", GenType: &gt},    // 1 AI chat add
		{Start: 4, End: 6, Type: "delete", AuthorType: "AI", GenType: &gt}, // 3 AI chat deletions
		{Start: 9, End: 10, Type: "delete", AuthorType: "Human"},           // 2 human deletions
	}}}}
	note.ByGenType = ByGenType{Chat: 99, Human: 99} // garbage prior value — must be discarded

	recomputeByGenType(note)

	if note.ByGenType.Chat != 4 { // 1 add + 3 deletes
		t.Errorf("chat: want 4, got %d", note.ByGenType.Chat)
	}
	if note.ByGenType.Human != 2 {
		t.Errorf("human: want 2 (human deletions), got %d", note.ByGenType.Human)
	}
}

// TestReconcileDeletesFromEdits_NoFalsePositive: a human-deleted line whose content
// no AI tool recorded stays Human.
func TestReconcileDeletesFromEdits_NoFalsePositive(t *testing.T) {
	db, err := store.OpenAt(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer db.Close()

	const repo, file = "/repo", "f.go"
	if _, err := db.InsertEdit(store.Edit{
		TimestampNanos: 1000, RepoPath: repo, FilePath: file,
		Tool: "copilot", Confidence: "high", GenType: "chat",
		RemovedLines: removedLines("aiRemovedThis()"),
	}); err != nil {
		t.Fatalf("InsertEdit: %v", err)
	}

	fe := &FileEntry{Path: file}
	delRange(fe, 5, 5)
	note := &Note{Schema: 2, Files: []FileEntry{*fe}}
	change := &CommitChange{
		Deleted: map[string][]DeletedLine{file: {{LineNum: 5, Content: "humanRemovedThis()"}}},
		Renames: map[string]string{},
	}

	reconcileDeletesFromEdits(db, repo, 2000, note, change)

	if r := delRangeAuthor(t, note, file, 5); r.AuthorType != "Human" {
		t.Errorf("deleted line 5: want Human (no matching AI removal), got %q", r.AuthorType)
	}
}
