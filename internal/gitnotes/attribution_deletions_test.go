package gitnotes

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blamely/blamely/internal/authorship"
	"github.com/blamely/blamely/internal/install"
)

// An AI-deleted line must be attributed AI in the commit note (deletion log),
// not Human (the legacy default).
func TestFlipDeletionsToWorkingLog(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	repo := t.TempDir()
	git := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "core.hooksPath="}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("checkout", "-q", "-b", "main")
	const rel = "f.txt"
	abs := filepath.Join(repo, rel)
	os.WriteFile(abs, []byte("keep1\ntodelete\nkeep2\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "c1")
	parent := git("rev-parse", "HEAD")

	// AI removes the middle line → records a deletion attributed to AI.
	if _, err := authorship.Update(repo, "main", parent, rel, "keep1\nkeep2\n", "keep1\ntodelete\nkeep2\n",
		authorship.Author{Type: authorship.AI, Tool: "claude", GenType: "chat"}, 1); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(abs, []byte("keep1\nkeep2\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "c2")
	sha := git("rev-parse", "HEAD")

	install.RemoveLegacyRepoHooks(repo)
	note, err := AttributeAndWrite(repo, sha)
	if err != nil {
		t.Fatal(err)
	}
	if note.Totals.AIDeletedLines != 1 || note.Totals.HumanDeletedLines != 0 {
		t.Errorf("totals: want ai_deleted=1 human_deleted=0, got ai=%d human=%d",
			note.Totals.AIDeletedLines, note.Totals.HumanDeletedLines)
	}
	var del *RangeEntry
	for fi := range note.Files {
		for i := range note.Files[fi].Lines {
			if r := &note.Files[fi].Lines[i]; r.Type == "delete" {
				del = r
			}
		}
	}
	if del == nil || del.AuthorType != "AI" || del.Tool != "claude" {
		t.Errorf("deleted line: want AI/claude, got %+v", del)
	}
}
