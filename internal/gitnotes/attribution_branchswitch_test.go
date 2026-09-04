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

// The reported workflow, end to end through the REAL post-commit sequence: build
// on master, then branch and commit there.
//
//	<AI edits app.py on master>     → working log under working_logs/master/<sha>/
//	git checkout -b feature         → HEAD unmoved, working tree unmoved
//	git commit                      → flip reads working_logs/feature/<sha>/
//
// The working log is keyed by branch, so the flip used to read an empty directory,
// keep each file's prior (Human skeleton) attribution, and ship the AI work as
// Human. Nothing about it was visible — the note simply said the human wrote it.
func TestAttributeAfterCheckoutB_KeepsAIAttribution(t *testing.T) {
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
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	git("checkout", "-q", "-b", "master")

	const rel = "app.py"
	abs := filepath.Join(repo, rel)
	os.WriteFile(abs, []byte("h1\nh2\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "c1")
	parent := git("rev-parse", "HEAD")

	// An agent appends a line while the user is still on master.
	if _, err := authorship.Update(repo, "master", parent, rel, "h1\nh2\nai3\n", "h1\nh2\n",
		authorship.Author{Type: authorship.AI, Tool: "claude", GenType: "chat"}, 1); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(abs, []byte("h1\nh2\nai3\n"), 0o644)

	// Time to commit — branch first. HEAD does not move, so `parent` stays the
	// commit both the working log and the new commit are based on.
	git("checkout", "-q", "-b", "feature")
	if got := git("rev-parse", "HEAD"); got != parent {
		t.Fatalf("checkout -b moved HEAD (%s → %s); the premise of this test is gone", parent, got)
	}
	git("add", ".")
	git("commit", "-q", "-m", "c2")
	sha := git("rev-parse", "HEAD")

	install.RemoveLegacyRepoHooks(repo)
	note, err := AttributeAndWrite(repo, sha)
	if err != nil {
		t.Fatalf("AttributeAndWrite: %v", err)
	}
	if note.Branch != "feature" {
		t.Fatalf("note branch = %q, want feature", note.Branch)
	}

	var found *RangeEntry
	for fi := range note.Files {
		if note.Files[fi].Path != rel {
			continue
		}
		for i := range note.Files[fi].Lines {
			if r := &note.Files[fi].Lines[i]; r.Type == "add" && r.Start <= 3 && 3 <= r.End {
				found = r
			}
		}
	}
	if found == nil {
		t.Fatalf("no add range covering line 3 in note: %+v", note.Files)
	}
	if found.AuthorType != "AI" || found.Tool != "claude" {
		t.Errorf("line 3 after `git checkout -b`: want AI/claude, got author_type=%q tool=%q — "+
			"the working log left under master/ was not adopted onto feature/", found.AuthorType, found.Tool)
	}
	if note.Totals.AILines == 0 {
		t.Errorf("note ai_lines = 0; the AI line was not counted in the aggregates")
	}
}
