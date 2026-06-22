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

// TestAttributePipelineE2E exercises the REAL post-commit attribute sequence —
// install.RemoveLegacyRepoHooks followed by AttributeAndWrite, exactly as the
// `blamely attribute` command runs them — against a real working log. This is the
// gap that let the legacy-cleanup bug through (the flip's own unit test bypasses
// RemoveLegacyRepoHooks): if cleanup ever wipes the working log again, this fails.
func TestAttributePipelineE2E(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv("BLAMELY_ATTRIBUTION_V2", "1")
	// Isolate the global store DB to a temp HOME (os.UserHomeDir honors HOME).
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
	const rel = "app.py"
	abs := filepath.Join(repo, rel)
	os.WriteFile(abs, []byte("h1\nh2\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "c1")
	parent := git("rev-parse", "HEAD")

	// Simulate capture: an AI appends a line; the working log (base = parent)
	// records lines 1-2 Human, line 3 AI.
	if _, err := authorship.Update(repo, "main", parent, rel, "h1\nh2\nai3\n", "h1\nh2\n",
		authorship.Author{Type: authorship.AI, Tool: "claude", GenType: "chat"}, 1); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(abs, []byte("h1\nh2\nai3\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "c2")
	sha := git("rev-parse", "HEAD")

	// EXACTLY what cmdAttribute does, in order.
	install.RemoveLegacyRepoHooks(repo)
	note, err := AttributeAndWrite(repo, sha)
	if err != nil {
		t.Fatalf("AttributeAndWrite: %v", err)
	}

	// The flip must have sourced the added line from the working log: AI/claude.
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
		t.Errorf("line 3: want AI/claude (flip from working log), got author_type=%q tool=%q — "+
			"working log likely wiped before the flip", found.AuthorType, found.Tool)
	}
}

// TestAttributePipelineE2E_TwoCommits runs two capture→commit→attribute cycles in
// one repo. The second cycle exercises GC (which ran after the first attribute) and
// a fresh working log at the new HEAD base — verifying a prior commit's attribution
// doesn't leak and the second flip still works.
func TestAttributePipelineE2E_TwoCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv("BLAMELY_ATTRIBUTION_V2", "1")
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
	const rel = "app.py"
	abs := filepath.Join(repo, rel)
	os.WriteFile(abs, []byte("h1\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "c1")

	aiAddedLine := func(commitMsg, prevContent, newContent string, line int) *RangeEntry {
		parent := git("rev-parse", "HEAD")
		if _, err := authorship.Update(repo, "main", parent, rel, newContent, prevContent,
			authorship.Author{Type: authorship.AI, Tool: "codex", GenType: "cli"}, 1); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(abs, []byte(newContent), 0o644)
		git("add", ".")
		git("commit", "-q", "-m", commitMsg)
		sha := git("rev-parse", "HEAD")
		install.RemoveLegacyRepoHooks(repo)
		note, err := AttributeAndWrite(repo, sha)
		if err != nil {
			t.Fatalf("AttributeAndWrite %s: %v", commitMsg, err)
		}
		for fi := range note.Files {
			if note.Files[fi].Path != rel {
				continue
			}
			for i := range note.Files[fi].Lines {
				if r := &note.Files[fi].Lines[i]; r.Type == "add" && r.Start <= line && line <= r.End {
					return r
				}
			}
		}
		return nil
	}

	// Cycle 1: AI appends line 2.
	r1 := aiAddedLine("c2", "h1\n", "h1\nai2\n", 2)
	if r1 == nil || r1.AuthorType != "AI" || r1.Tool != "codex" {
		t.Fatalf("cycle 1 line 2: want AI/codex, got %+v", r1)
	}
	// Cycle 2: AI appends line 3 (new working log at the new HEAD base).
	r2 := aiAddedLine("c3", "h1\nai2\n", "h1\nai2\nai3\n", 3)
	if r2 == nil || r2.AuthorType != "AI" || r2.Tool != "codex" {
		t.Fatalf("cycle 2 line 3: want AI/codex, got %+v", r2)
	}
}
