package gitnotes

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blamely/blamely/internal/authorship"
	"github.com/blamely/blamely/internal/gitutil"
	"github.com/blamely/blamely/internal/store"
)

// TestReconcileGutterOverrides_DuplicateContentBlock is the live-gutter twin of
// TestReconcileAddsFromEdits_DuplicateContentBlock: an AI chat tool adds (in the
// WORKING TREE, uncommitted) a block whose lines duplicate a pre-existing sibling
// block. The working-log fold leaves the new lines Human; the recorded content_shas
// reattribute them, and the blank separator inherits its AI neighbour.
func TestReconcileGutterOverrides_DuplicateContentBlock(t *testing.T) {
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

	const rel = "styles.css"
	abs := filepath.Join(repo, rel)
	// Base commit: a github block (human).
	base := "" +
		".github-btn {\n" +
		"  display: flex;\n" +
		"  color: #fff;\n" +
		"}\n"
	os.WriteFile(abs, []byte(base), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "c1")
	headSHA := git("rev-parse", "HEAD")

	// Working tree (uncommitted): prepend an AI-generated okta block whose middle
	// lines duplicate the github block, plus a blank separator.
	okta := []string{".okta-btn {", "  display: flex;", "  color: #fff;", "}", "", ".okta-icon {", "  width: 18px;", "}"}
	current := strings.Join(okta, "\n") + "\n" + base
	os.WriteFile(abs, []byte(current), 0o644)

	// The working-log fold (whole file) leaves the okta lines Human — simulate by
	// recording all-human authorship for the current buffer.
	if _, err := authorship.Update(repo, "main", headSHA, rel, current, base,
		authorship.Author{Type: authorship.Human, GenType: "human"}, 1); err != nil {
		t.Fatal(err)
	}

	// SQLite: the AI chat edit recorded the okta block with PLACEHOLDER positions
	// (blank line skipped), at a timestamp after HEAD was committed.
	headNanos, _ := CommitTimestampNanos(repo, headSHA)
	db, err := store.OpenAt(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	repoID, _ := gitutil.RepoID(repo) // resolved id the daemon records & the gutter queries
	if _, err := db.InsertEdit(store.Edit{
		TimestampNanos: headNanos + 1_000_000_000,
		RepoPath:       repoID, FilePath: rel,
		Tool: "copilot", Confidence: "high", GenType: "chat",
		Lines: editLines(okta...),
	}); err != nil {
		t.Fatalf("InsertEdit: %v", err)
	}

	wl, err := authorship.LoadWorkingLog(repo, "main", headSHA, rel)
	if err != nil || wl == nil {
		t.Fatalf("LoadWorkingLog: wl=%v err=%v", wl, err)
	}
	changed := UncommittedAddedLines(repo, rel)

	overrides := ReconcileGutterOverrides(db, repo, headSHA, wl, changed)
	db.Close()

	if len(changed) == 0 {
		t.Fatal("expected git diff HEAD to report the prepended okta block as changed")
	}
	// The whole uncommitted block is AI: every changed line — including the blank
	// separator (inherited) — must carry an AI/copilot/chat override, and nothing
	// outside the changed set may be overridden.
	for ln := range changed {
		a, ok := overrides[ln]
		if !ok {
			t.Errorf("changed line %d: expected an AI override, got none", ln)
			continue
		}
		if a.Type != authorship.AI || a.Tool != "copilot" || a.GenType != "chat" {
			t.Errorf("changed line %d: want AI/copilot/chat, got %+v", ln, a)
		}
	}
	if len(overrides) != len(changed) {
		t.Errorf("overrides should cover exactly the changed lines: %d overrides vs %d changed",
			len(overrides), len(changed))
	}
}
