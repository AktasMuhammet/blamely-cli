package gitnotes

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// rebaseInteractive runs a REAL `git rebase -i HEAD~2` in repo, rewriting the
// second todo line's `pick` to `action` (squash/fixup) via GIT_SEQUENCE_EDITOR.
// GIT_EDITOR=true accepts the default combined commit message.
func rebaseInteractive(t *testing.T, repo, action string) {
	t.Helper()
	seq := filepath.Join(t.TempDir(), "seq.sh")
	script := "#!/bin/sh\nawk 'NR==2 { sub(/^pick/, \"" + action + "\") } { print }' \"$1\" > \"$1.tmp\" && mv \"$1.tmp\" \"$1\"\n"
	if err := os.WriteFile(seq, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", repo, "-c", "core.hooksPath=", "rebase", "-i", "HEAD~2")
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_SEQUENCE_EDITOR="+seq, "GIT_EDITOR=true")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git rebase -i (%s): %v\n%s", action, err, out)
	}
}

// squashFixture builds: c1 base → c2 (AI line, noted) → c3 (human line, noted).
// Returns the two source shas.
func squashFixture(t *testing.T) (repo string, git func(...string) string, c2, c3 string) {
	repo, git = replayE2E(t)
	abs := filepath.Join(repo, "app.py")
	os.WriteFile(abs, []byte("l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "c1")

	insertEdit(t, repo, "app.py", "claude", "chat", time.Now().UnixNano(),
		"l1", "l2", "l3", "l4", "l5", "l6", "l7", "l8", "ai9")
	os.WriteFile(abs, []byte("l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nai9\n"), 0o644)
	time.Sleep(1100 * time.Millisecond)
	git("add", ".")
	git("commit", "-q", "-m", "c2-ai")
	c2 = git("rev-parse", "HEAD")
	note := attribute(t, repo, c2)
	if r := addRangeFor(note, "app.py", 9); r == nil || r.AuthorType != "AI" {
		t.Fatalf("setup: c2 line 9 should be AI, got %+v", r)
	}

	os.WriteFile(abs, []byte("l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nai9\nhuman10\n"), 0o644)
	time.Sleep(1100 * time.Millisecond)
	git("add", ".")
	git("commit", "-q", "-m", "c3-human")
	c3 = git("rev-parse", "HEAD")
	attribute(t, repo, c3)
	return repo, git, c2, c3
}

// TestPostRewrite_SquashMergesNotes: interactive-rebase squash of c3 into c2 —
// the post-rewrite handler must leave ONE parseable note on the fold that keeps
// the AI line AI/claude and the human line Human.
func TestPostRewrite_SquashMergesNotes(t *testing.T) {
	repo, git, c2, c3 := squashFixture(t)
	rebaseInteractive(t, repo, "squash")
	newSHA := git("rev-parse", "HEAD")

	// What git would feed the post-rewrite hook: both sources map to the fold.
	HandlePostRewrite(repo, "rebase", []RewritePair{{Old: c2, New: newSHA}, {Old: c3, New: newSHA}})

	note := loadNoteForSeed(repo, newSHA)
	if note == nil {
		t.Fatal("squashed commit has no parseable note after post-rewrite")
	}
	aiLn := lineOf(t, repo, "app.py", "ai9")
	humanLn := lineOf(t, repo, "app.py", "human10")
	if r := addRangeFor(note, "app.py", aiLn); r == nil || r.AuthorType != "AI" || r.Tool != "claude" {
		t.Errorf("squashed AI line: want AI/claude, got %+v", r)
	}
	if r := addRangeFor(note, "app.py", humanLn); r == nil || r.AuthorType != "Human" {
		t.Errorf("squashed human line: want Human, got %+v", r)
	}
}

// TestPostRewrite_FixupDropsNothing: fixup (discards c3's message) must still
// preserve both lines' attribution — the mapping, not the message, drives it.
func TestPostRewrite_FixupDropsNothing(t *testing.T) {
	repo, git, c2, c3 := squashFixture(t)
	rebaseInteractive(t, repo, "fixup")
	newSHA := git("rev-parse", "HEAD")

	HandlePostRewrite(repo, "rebase", []RewritePair{{Old: c2, New: newSHA}, {Old: c3, New: newSHA}})

	note := loadNoteForSeed(repo, newSHA)
	if note == nil {
		t.Fatal("fixup commit has no parseable note after post-rewrite")
	}
	aiLn := lineOf(t, repo, "app.py", "ai9")
	humanLn := lineOf(t, repo, "app.py", "human10")
	if r := addRangeFor(note, "app.py", aiLn); r == nil || r.AuthorType != "AI" || r.Tool != "claude" {
		t.Errorf("fixup AI line: want AI/claude, got %+v", r)
	}
	if r := addRangeFor(note, "app.py", humanLn); r == nil || r.AuthorType != "Human" {
		t.Errorf("fixup human line: want Human, got %+v", r)
	}
}

// TestPostRewrite_AmendNoop: a 1→1 rewrite whose copied note is already valid
// must be left alone (no rebuild).
func TestPostRewrite_AmendNoop(t *testing.T) {
	repo, git := replayE2E(t)
	abs := filepath.Join(repo, "app.py")
	os.WriteFile(abs, []byte("h1\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "c1")

	insertEdit(t, repo, "app.py", "claude", "chat", time.Now().UnixNano(), "h1", "ai2")
	os.WriteFile(abs, []byte("h1\nai2\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "c2")
	old := git("rev-parse", "HEAD")
	attribute(t, repo, old)

	git("commit", "-q", "--amend", "-m", "c2-amended")
	amended := git("rev-parse", "HEAD")
	// git copies the note onto the amended sha (notes.rewriteRef was configured
	// by the attribute run above); the handler must leave it untouched.
	if loadNoteForSeed(repo, amended) == nil {
		t.Skip("git did not copy the note on amend in this environment")
	}
	HandlePostRewrite(repo, "amend", []RewritePair{{Old: old, New: amended}})

	note := loadNoteForSeed(repo, amended)
	if note == nil {
		t.Fatal("amended commit lost its note")
	}
	if r := addRangeFor(note, "app.py", 2); r == nil || r.AuthorType != "AI" || r.Tool != "claude" {
		t.Errorf("amended line 2: want AI/claude preserved, got %+v", r)
	}
}

func TestParseRewritePairs(t *testing.T) {
	shaA := strings.Repeat("a", 40)
	shaB := strings.Repeat("b", 40)
	shaC := strings.Repeat("c", 40)
	in := shaA + " " + shaC + "\n" +
		shaB + " " + shaC + " extra-field\n" + // extra fields tolerated
		"garbage line\n" + // skipped
		"short " + shaC + "\n" // skipped (short old sha)
	pairs := ParseRewritePairs(strings.NewReader(in))
	if len(pairs) != 2 {
		t.Fatalf("pairs = %d, want 2: %+v", len(pairs), pairs)
	}
	if pairs[0] != (RewritePair{Old: shaA, New: shaC}) || pairs[1] != (RewritePair{Old: shaB, New: shaC}) {
		t.Errorf("unexpected pairs: %+v", pairs)
	}
}
