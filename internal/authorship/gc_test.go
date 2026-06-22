package authorship

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// GCWorkingLogs must prune a log whose base object is gone, while keeping a log at
// the real HEAD and a non-SHA base (INITIAL).
func TestGCWorkingLogs(t *testing.T) {
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
	os.WriteFile(filepath.Join(repo, "seed"), []byte("x\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "c1")
	head := git("rev-parse", "HEAD")

	// A log at the real HEAD (must survive), a log at a bogus/dangling base (must be
	// pruned), and a non-SHA base (INITIAL, must survive).
	dangling := strings.Repeat("a", 40)
	if _, err := Update(repo, "main", head, "f.txt", "a\n", "", HumanAuthor(), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := Update(repo, "main", dangling, "g.txt", "b\n", "", HumanAuthor(), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := Update(repo, "main", "INITIAL", "h.txt", "c\n", "", HumanAuthor(), 1); err != nil {
		t.Fatal(err)
	}

	pruned, err := GCWorkingLogs(repo)
	if err != nil {
		t.Fatal(err)
	}
	if pruned != 1 {
		t.Errorf("pruned: got %d, want 1", pruned)
	}
	exists := func(branch, base string) bool {
		_, e := os.Stat(workingLogDir(repo, branch, base))
		return e == nil
	}
	if !exists("main", head) {
		t.Error("HEAD-based log must survive")
	}
	if exists("main", dangling) {
		t.Error("dangling-base log must be pruned")
	}
	if !exists("main", "INITIAL") {
		t.Error("INITIAL (non-SHA) base must survive")
	}
}
