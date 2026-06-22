package gitnotes

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blamely/blamely/internal/authorship"
)

// SeedCommittedWorkingLog must reconstruct committed authorship from blame + notes:
// a line the note marks AI seeds as AI; an unmarked line seeds as Human. This is
// what lets a post-commit edit keep unchanged committed lines' real authors.
func TestSeedCommittedWorkingLog(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
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
	os.WriteFile(filepath.Join(repo, "f.txt"), []byte("ai line\nhuman line\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "c1")
	head := git("rev-parse", "HEAD")

	// Note for the commit: line 1 is AI (claude), line 2 is unlisted (→ human).
	note := Note{
		Commit: head, Branch: "main",
		Files: []FileEntry{{
			Path: "f.txt", Type: "ADDED", Added: 2,
			Lines: []RangeEntry{{Start: 1, End: 1, Type: "add", AuthorType: "AI", Tool: "claude"}},
		}},
	}
	body, _ := json.Marshal(note)
	noteFile := filepath.Join(t.TempDir(), "note.json")
	os.WriteFile(noteFile, body, 0o644)
	git("notes", "--ref="+NotesRef, "add", "-f", "-F", noteFile, head)

	if err := SeedCommittedWorkingLog(repo, "main", head, "f.txt"); err != nil {
		t.Fatal(err)
	}

	wl, err := authorship.LoadWorkingLog(repo, "main", head, "f.txt")
	if err != nil || wl == nil {
		t.Fatalf("seeded log missing: %v", err)
	}
	got := map[int]authorship.AuthorType{}
	tool := map[int]string{}
	for _, r := range wl.Lines {
		for ln := r.Start; ln <= r.End; ln++ {
			got[ln] = r.Author.Type
			tool[ln] = r.Author.Tool
		}
	}
	if got[1] != authorship.AI || tool[1] != "claude" {
		t.Errorf("line 1: want AI claude, got %q %q", got[1], tool[1])
	}
	if got[2] != authorship.Human {
		t.Errorf("line 2: want Human, got %q", got[2])
	}

	// Idempotent: a second seed must not clobber the existing log.
	if err := SeedCommittedWorkingLog(repo, "main", head, "f.txt"); err != nil {
		t.Fatal(err)
	}
}
