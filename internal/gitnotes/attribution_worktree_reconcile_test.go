package gitnotes

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blamely/blamely/internal/gitutil"
	"github.com/blamely/blamely/internal/store"
)

// TestAttributeWorkingTree_ReconcilesFromEdits is the pre-commit twin of the
// commit-note reconciliation: `blamely stats` (→ AttributeWorkingTree) must show
// the SAME AI attribution the eventual commit note will. With no working-log/
// deletions-log entries, the fold leaves the uncommitted add+delete Human; the
// SQLite content_shas recorded by the AI tool reattribute them.
func TestAttributeWorkingTree_ReconcilesFromEdits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	repo := t.TempDir()
	git := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "core.hooksPath="}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("checkout", "-q", "-b", "main")

	const rel = "page.html"
	abs := filepath.Join(repo, rel)
	// Base: a github block + a line that will be deleted.
	base := ".github-btn { color: #fff; }\nDELETE ME\n"
	os.WriteFile(abs, []byte(base), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "c1")

	// Working tree (uncommitted): prepend an AI okta block, remove "DELETE ME".
	okta := []string{".okta-btn {", "  color: #fff;", "}"}
	current := strings.Join(okta, "\n") + "\n.github-btn { color: #fff; }\n"
	os.WriteFile(abs, []byte(current), 0o644)

	// SQLite: the AI chat edit recorded the okta adds (placeholder positions) and the
	// removed "DELETE ME" line.
	db, err := store.Open()
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	repoID, _ := gitutil.RepoID(repo)
	if _, err := db.InsertEdit(store.Edit{
		TimestampNanos: 1_000_000_000,
		RepoPath:       repoID, FilePath: rel,
		Tool: "copilot", Confidence: "high", GenType: "chat",
		Lines:        editLines(okta...),
		RemovedLines: removedLines("DELETE ME"),
	}); err != nil {
		t.Fatalf("InsertEdit: %v", err)
	}
	db.Close()

	note, err := AttributeWorkingTree(repo)
	if err != nil {
		t.Fatalf("AttributeWorkingTree: %v", err)
	}

	if note.Totals.AILines != 3 || note.Totals.HumanLines != 0 {
		t.Errorf("added: want AI=3 Human=0, got AI=%d Human=%d", note.Totals.AILines, note.Totals.HumanLines)
	}
	if note.Totals.AIDeletedLines != 1 || note.Totals.HumanDeletedLines != 0 {
		t.Errorf("deleted: want AI=1 Human=0, got AI=%d Human=%d",
			note.Totals.AIDeletedLines, note.Totals.HumanDeletedLines)
	}
	if note.ByTool["copilot"].Lines != 3 || note.ByTool["copilot"].DeletedLines != 1 {
		t.Errorf("by_tool copilot: want lines=3 deleted=1, got lines=%d deleted=%d",
			note.ByTool["copilot"].Lines, note.ByTool["copilot"].DeletedLines)
	}
}
