package gitnotes

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/blamely/blamely/internal/store"
)

func tokEdit(repo, file, tool string, ts, in, out int64) store.Edit {
	return store.Edit{
		TimestampNanos: ts, RepoPath: repo, FilePath: file,
		Tool: store.Tool(tool), Confidence: "high", GenType: "chat",
		InputTokens:  sql.NullInt64{Int64: in, Valid: true},
		OutputTokens: sql.NullInt64{Int64: out, Valid: true},
	}
}

// TestApplyEditTokens_DedupsPerTurn verifies tokens are summed once per turn
// (tool+ts), so a Codex turn that stamped the same usage on every file it edited
// isn't multiplied by the file count.
func TestApplyEditTokens_DedupsPerTurn(t *testing.T) {
	db, err := store.OpenAt(filepath.Join(t.TempDir(), "t.sqlite"))
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer db.Close()

	const repo = "/repo"
	// One codex turn (ts=1000) touched fileA AND fileB with identical usage.
	for _, e := range []store.Edit{
		tokEdit(repo, "a.txt", "codex", 1000, 100, 10),
		tokEdit(repo, "b.txt", "codex", 1000, 100, 10), // same turn — must not double-count
		tokEdit(repo, "a.txt", "codex", 2000, 50, 5),   // a later codex turn
		tokEdit(repo, "a.txt", "claude", 3000, 200, 20),
	} {
		if _, err := db.InsertEdit(e); err != nil {
			t.Fatal(err)
		}
	}

	note := &Note{Schema: 2, Files: []FileEntry{{Path: "a.txt"}, {Path: "b.txt"}},
		ByTool: map[string]Tool{"codex": {Lines: 3}, "claude": {Lines: 1}}}

	applyEditTokens(db, repo, 9000, note)

	if got := note.ByTool["codex"].Tokens; got == nil || got.Input != 150 || got.Output != 15 {
		t.Errorf("codex tokens: want in=150 out=15 (turn-deduped), got %+v", got)
	}
	if got := note.ByTool["claude"].Tokens; got == nil || got.Input != 200 {
		t.Errorf("claude tokens: want in=200, got %+v", got)
	}
	if note.Totals.Tokens == nil || note.Totals.Tokens.Input != 350 || note.Totals.Tokens.Output != 35 {
		t.Errorf("totals: want in=350 out=35, got %+v", note.Totals.Tokens)
	}
}

// TestApplyEditTokens_SkipsNonContributingTool: a tool with in-window token edits
// but no committed lines (not in by_tool) is not credited.
func TestApplyEditTokens_SkipsNonContributingTool(t *testing.T) {
	db, err := store.OpenAt(filepath.Join(t.TempDir(), "t.sqlite"))
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer db.Close()
	const repo = "/repo"
	if _, err := db.InsertEdit(tokEdit(repo, "a.txt", "gemini", 1000, 500, 50)); err != nil {
		t.Fatal(err)
	}
	note := &Note{Schema: 2, Files: []FileEntry{{Path: "a.txt"}}, ByTool: map[string]Tool{}}
	applyEditTokens(db, repo, 9000, note)
	if note.Totals.Tokens != nil {
		t.Errorf("no contributing tool → no tokens, got %+v", note.Totals.Tokens)
	}
}
