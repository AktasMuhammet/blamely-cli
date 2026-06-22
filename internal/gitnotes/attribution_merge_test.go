package gitnotes

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// restrictMergeToResolution must keep only the conflict-resolution line (added vs
// BOTH parents) in a merge commit's change set, dropping each branch's own lines.
func TestRestrictMergeToResolution(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
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
	write := func(s string) { os.WriteFile(filepath.Join(repo, "f.txt"), []byte(s), 0o644) }

	git("init", "-q")
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	git("checkout", "-q", "-b", "main")
	write("base\n")
	git("add", ".")
	git("commit", "-q", "-m", "c1")

	git("checkout", "-q", "-b", "feature")
	write("base\nfeature_line\n")
	git("add", ".")
	git("commit", "-q", "-m", "feat")

	git("checkout", "-q", "main")
	write("base\nmain_line\n")
	git("add", ".")
	git("commit", "-q", "-m", "mainwork")

	// Merge feature → conflict at line 2 (non-zero exit is expected); resolve to keep
	// both branches' lines + a new resolution line, then commit the merge.
	mergeCmd := exec.Command("git", "-C", repo, "-c", "core.hooksPath=", "merge", "--no-commit", "feature")
	mergeCmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	_, _ = mergeCmd.CombinedOutput() // conflict → non-zero, expected
	write("base\nmain_line\nfeature_line\nresolved_line\n")
	git("add", ".")
	git("commit", "--no-verify", "-m", "merge")
	sha := git("rev-parse", "HEAD")
	if parents := git("rev-list", "--parents", "-n", "1", sha); len(strings.Fields(parents)) != 3 {
		t.Fatalf("expected a merge commit (2 parents), got: %s", parents)
	}

	change, err := DiffCommit(repo, sha)
	if err != nil {
		t.Fatal(err)
	}
	restrictMergeToResolution(repo, sha, change)

	var got []string
	for _, a := range change.Added {
		got = append(got, strings.TrimSpace(a.Content))
	}
	// Only the resolution line survives; main_line/feature_line came from a parent.
	if len(got) != 1 || got[0] != "resolved_line" {
		t.Errorf("merge note should keep only the resolution line, got %v", got)
	}
}
