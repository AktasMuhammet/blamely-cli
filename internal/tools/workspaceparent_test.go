package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/blamely/blamely/internal/gitutil"
)

// gitInWorkspace initialises a repo at ws/name with one committed file and
// returns its resolved root, so a test can build the layout that broke:
//
//	workspace/            ← agent's cwd, NOT a git repo
//	  backend/            ← its own clone
//	  frontend/           ← another clone
func gitInWorkspace(t *testing.T, ws, name, file, content string) string {
	t.Helper()
	root := filepath.Join(ws, name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init")
	run("config", "core.autocrlf", "false")
	if err := os.WriteFile(filepath.Join(root, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "seed")
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	return root
}

// A Bash command run from a workspace dir ABOVE the repos used to record
// nothing at all: `git rev-parse --show-toplevel` on the cwd fails there, so
// every shell-written file fell back to Human at commit. Each nested repo must
// now produce its own payloads, under its OWN repo id.
func TestClaudeBashWrites_FromWorkspaceParent(t *testing.T) {
	ws := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(ws); err == nil {
		ws = resolved
	}
	backend := gitInWorkspace(t, ws, "backend", "app.py", "a\n")
	frontend := gitInWorkspace(t, ws, "frontend", "index.js", "x\n")

	// The command wrote into both repos (as a codegen script would).
	if err := os.WriteFile(filepath.Join(backend, "app.py"), []byte("a\nbash_line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(frontend, "index.js"), []byte("x\nbash_line\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	roots := gitutil.DiscoverRepos(ws)
	if len(roots) != 2 {
		t.Fatalf("DiscoverRepos(%q) = %v, want both repos", ws, roots)
	}

	byRepo := map[string]string{}
	for _, root := range roots {
		for _, p := range claudeBashWritePayloads(root, "sess-1", "", "chat") {
			byRepo[p.RepoPath] = p.FilePath
		}
	}
	if got := byRepo[backend]; got != "app.py" {
		t.Errorf("backend payload = %q, want app.py (all payloads: %v)", got, byRepo)
	}
	if got := byRepo[frontend]; got != "index.js" {
		t.Errorf("frontend payload = %q, want index.js (all payloads: %v)", got, byRepo)
	}

	// The full path must stay best-effort: no daemon is running in tests, and a
	// hook that errors can abort the host's whole hook chain.
	if err := recordClaudeBashWrites(ws, "sess-1", "", "chat", "python gen.py"); err != nil {
		t.Fatalf("recordClaudeBashWrites: %v", err)
	}
}

// A shell deletion issued from the workspace parent must be credited in the
// repo that actually owns the removed file.
func TestRecordShellDeletionsFrom_WorkspaceParent(t *testing.T) {
	ws := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(ws); err == nil {
		ws = resolved
	}
	backend := gitInWorkspace(t, ws, "backend", "old.py", "l1\nl2\n")
	if err := os.Remove(filepath.Join(backend, "old.py")); err != nil {
		t.Fatal(err)
	}

	// Resolution the recorder performs: discover the repo below cwd, then match
	// git's deleted-file list against the command's targets.
	var credited []string
	targets := shellDeleteTargets("rm backend/old.py")
	for _, root := range gitutil.DiscoverRepos(ws) {
		for _, rel := range gitDeletedFiles(root) {
			if MatchesFileOp(rel, targets) {
				credited = append(credited, filepath.Join(root, rel))
			}
		}
	}
	want := filepath.Join(backend, "old.py")
	if len(credited) != 1 || credited[0] != want {
		t.Fatalf("credited = %v, want [%q]", credited, want)
	}
	if err := recordShellDeletionsFrom(ws, "rm backend/old.py", "claude", "chat", "", "s", "", "claude_bash_delete"); err != nil {
		t.Fatalf("recordShellDeletionsFrom: %v", err)
	}
}
