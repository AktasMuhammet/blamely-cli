package gitnotes

import (
	"path/filepath"
	"testing"

	"github.com/blamely/blamely/internal/store"
)

func sugEdit(repo, file, tool string, ts, suggested int64) store.Edit {
	return store.Edit{
		TimestampNanos: ts, RepoPath: repo, FilePath: file,
		Tool: store.Tool(tool), Confidence: "high", GenType: "chat",
		SuggestedLines: suggested,
	}
}

// TestApplyEditSuggested_BackfillsPerTool verifies suggested_lines is restored
// onto by_tool from the in-window edits after the working-log flip (which
// records which tool authored each line but not the proposal size). Deduped per
// edit row so an edit spanning several committed lines counts once.
func TestApplyEditSuggested_BackfillsPerTool(t *testing.T) {
	db, err := store.OpenAt(filepath.Join(t.TempDir(), "t.sqlite"))
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer db.Close()

	const repo = "/repo"
	for _, e := range []store.Edit{
		sugEdit(repo, "a.txt", "claude", 1000, 4),
		sugEdit(repo, "a.txt", "claude", 2000, 6), // a later edit, also measured
		sugEdit(repo, "b.txt", "codex", 1500, 10),
	} {
		if _, err := db.InsertEdit(e); err != nil {
			t.Fatal(err)
		}
	}

	note := &Note{Schema: 2, Files: []FileEntry{{Path: "a.txt"}, {Path: "b.txt"}},
		ByTool: map[string]Tool{"claude": {Lines: 4}, "codex": {Lines: 10}}}

	applyEditSuggested(db, repo, 9000, note)

	if got := note.ByTool["claude"].SuggestedLines; got != 10 {
		t.Errorf("claude suggested: want 10 (4+6), got %d", got)
	}
	if got := note.ByTool["codex"].SuggestedLines; got != 10 {
		t.Errorf("codex suggested: want 10, got %d", got)
	}
}

// TestApplyEditSuggested_SkipsNonContributingTool: a tool with in-window
// suggested edits but no committed lines (absent from by_tool) is not credited.
func TestApplyEditSuggested_SkipsNonContributingTool(t *testing.T) {
	db, err := store.OpenAt(filepath.Join(t.TempDir(), "t.sqlite"))
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer db.Close()
	const repo = "/repo"
	if _, err := db.InsertEdit(sugEdit(repo, "a.txt", "gemini", 1000, 12)); err != nil {
		t.Fatal(err)
	}
	note := &Note{Schema: 2, Files: []FileEntry{{Path: "a.txt"}}, ByTool: map[string]Tool{}}
	applyEditSuggested(db, repo, 9000, note)
	if _, ok := note.ByTool["gemini"]; ok {
		t.Errorf("non-contributing tool must not be added to by_tool")
	}
}
