package gitnotes

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blamely/blamely/internal/authorship"
)

// computeV2Divergence must compare the v1 note's added-line attribution against
// the working log and flag exactly the lines that disagree — the dual-run signal.
// Scenario: v1 (the old engine) mislabels all 3 added lines Human; the working log
// has L1/L2 Human + L3 AI, so the comparator reports 1 divergent line that the
// flip will fix.
func TestComputeV2Divergence(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	git := func(args ...string) string {
		// Disable any globally-configured hooks (the dev machine may have a
		// blamely post-commit hook) so this unit test is hermetic.
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
	os.WriteFile(filepath.Join(repo, "seed"), []byte("x\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "c1")
	parent := git("rev-parse", "HEAD") // base the working logs are keyed by

	// Build the working log at the parent base: human types two lines, AI appends.
	const rel = "f.txt"
	abs := filepath.Join(repo, rel)
	os.WriteFile(abs, []byte("h1\nh2\n"), 0o644)
	if _, err := authorship.Update(repo, "main", parent, rel, "h1\nh2\n", "", authorship.HumanAuthor(), 1); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(abs, []byte("h1\nh2\nai3\n"), 0o644)
	if _, err := authorship.Update(repo, "main", parent, rel, "h1\nh2\nai3\n", "",
		authorship.Author{Type: authorship.AI, Tool: "claude", GenType: "chat"}, 2); err != nil {
		t.Fatal(err)
	}

	// Commit the file; this is the commit the note describes.
	git("add", ".")
	git("commit", "-q", "-m", "c2")
	commit := git("rev-parse", "HEAD")

	// v1 note: pretend the OLD engine mislabeled all three added lines Human.
	note := &Note{
		Commit: commit,
		Branch: "main",
		Files: []FileEntry{{
			Path: rel,
			Type: "ADDED",
			Lines: []RangeEntry{
				{Start: 1, End: 3, Type: "add", AuthorType: "Human"},
			},
		}},
	}

	d := computeV2Divergence(repo, note)
	if d.Compared != 3 {
		t.Errorf("compared: want 3, got %d", d.Compared)
	}
	if d.Divergent != 1 {
		t.Errorf("divergent: want 1 (L3 v1=Human v2=AI), got %d", d.Divergent)
	}
	if d.V1AI != 0 || d.V2AI != 1 {
		t.Errorf("ai counts: want v1=0 v2=1, got v1=%d v2=%d", d.V1AI, d.V2AI)
	}
	if d.Files != 1 {
		t.Errorf("files: want 1, got %d", d.Files)
	}
}
