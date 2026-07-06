package gitnotes

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blamely/blamely/internal/authorship"
	"github.com/blamely/blamely/internal/gitutil"
	"github.com/blamely/blamely/internal/install"
	"github.com/blamely/blamely/internal/store"
)

// ── Phase 0: pin git's marker timing at post-commit ───────────────────────────
//
// The replay-detection design (detectReplayOp) branches on which git state is
// still visible when the post-commit hook fires. These tests RECORD that
// behavior against the real git on the machine — if a future git version moves
// the cleanup, they fail loudly and the detection order must be revisited.
//
// Pinned behavior (git 2.51):
//   • CHERRY_PICK_HEAD IS present at post-commit time for a clean cherry-pick
//     (with or without -x), and contains the SOURCE commit sha → primary
//     cherry-pick detection is the marker; the -x trailer is the fallback.
//   • SQUASH_MSG is ALREADY REMOVED at post-commit time after
//     `git merge --squash` + `git commit` → squash-merge detection must parse
//     the default "Squashed commit of the following:" commit message.

// replayProbeRepo builds a temp repo whose post-commit hook records the
// presence of CHERRY_PICK_HEAD / SQUASH_MSG (and CHERRY_PICK_HEAD's content)
// into probePath at the exact moment production's `blamely attribute` would run.
func replayProbeRepo(t *testing.T) (repo, probePath string, git func(args ...string) string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo = t.TempDir()
	probePath = filepath.Join(t.TempDir(), "probe.log")
	hooksDir := filepath.Join(repo, ".git-test-hooks")

	git = func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "core.hooksPath=" + hooksDir}, args...)...)
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

	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hook := `#!/bin/sh
g=$(git rev-parse --absolute-git-dir)
{
  echo "subject=$(git log -1 --format=%s)"
  if [ -f "$g/CHERRY_PICK_HEAD" ]; then
    echo "cherry_pick_head=$(cat "$g/CHERRY_PICK_HEAD")"
  else
    echo "cherry_pick_head="
  fi
  [ -f "$g/SQUASH_MSG" ] && echo "squash_msg=present" || echo "squash_msg=absent"
} >> "` + probePath + `"
`
	if err := os.WriteFile(filepath.Join(hooksDir, "post-commit"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	return repo, probePath, git
}

// probeEntries parses probe.log into one map per commit.
func probeEntries(t *testing.T, probePath string) []map[string]string {
	t.Helper()
	data, err := os.ReadFile(probePath)
	if err != nil {
		t.Fatalf("read probe: %v", err)
	}
	var out []map[string]string
	var cur map[string]string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if k == "subject" {
			cur = map[string]string{}
			out = append(out, cur)
		}
		if cur != nil {
			cur[k] = v
		}
	}
	return out
}

func TestReplayMarkerTiming_CherryPickHeadPresentAtPostCommit(t *testing.T) {
	repo, probe, git := replayProbeRepo(t)

	f := filepath.Join(repo, "f.txt")
	os.WriteFile(f, []byte("base\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "feature")
	os.WriteFile(f, []byte("base\nai line\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "feat-ai")
	srcSHA := git("rev-parse", "HEAD")
	git("checkout", "-q", "main")

	git("cherry-pick", srcSHA)

	entries := probeEntries(t, probe)
	last := entries[len(entries)-1]
	if last["subject"] != "feat-ai" {
		t.Fatalf("last probed commit = %q, want the cherry-picked feat-ai", last["subject"])
	}
	// The whole design of detectReplayOp's primary cherry-pick branch rests on this:
	if last["cherry_pick_head"] == "" {
		t.Fatalf("CHERRY_PICK_HEAD absent at post-commit time — git changed its cleanup " +
			"timing; cherry-pick detection must switch to the -x trailer as primary")
	}
	if last["cherry_pick_head"] != srcSHA {
		t.Errorf("CHERRY_PICK_HEAD content = %q, want source sha %q", last["cherry_pick_head"], srcSHA)
	}
}

func TestReplayMarkerTiming_SquashMsgGoneButMessageParseable(t *testing.T) {
	repo, probe, git := replayProbeRepo(t)

	f := filepath.Join(repo, "f.txt")
	os.WriteFile(f, []byte("base\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "feature")
	os.WriteFile(f, []byte("base\nai line\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "feat-ai")
	srcSHA := git("rev-parse", "HEAD")
	git("checkout", "-q", "main")

	git("merge", "--squash", "feature")
	git("commit", "-q", "--no-edit")

	entries := probeEntries(t, probe)
	last := entries[len(entries)-1]
	// Pin: SQUASH_MSG is already unlinked when post-commit fires (git 2.51), so
	// the detection must rely on the default commit-message shape below. If a
	// future git keeps the file around, that's a strictly better signal — flag it
	// so the detection order can be upgraded.
	if last["squash_msg"] == "present" {
		t.Log("NOTE: SQUASH_MSG now survives to post-commit — detectReplayOp could prefer it")
	}

	// The default squash message must carry the source commit shas.
	msg := git("log", "-1", "--format=%B")
	if !strings.Contains(msg, "Squashed commit of the following:") {
		t.Fatalf("default squash message shape changed:\n%s", msg)
	}
	if !strings.Contains(msg, "commit "+srcSHA) {
		t.Fatalf("squash message does not list source commit %s:\n%s", srcSHA, msg)
	}
}

// ── Phase 1: e2e — attribution follows the change ─────────────────────────────

// replayE2E builds the standard isolated e2e environment: temp HOME (store DB),
// temp repo with a main branch, and the git helper. Mirrors the harness of
// attribution_e2e_test.go.
func replayE2E(t *testing.T) (repo string, git func(args ...string) string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	repo = t.TempDir()
	git = func(args ...string) string {
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
	git("checkout", "-q", "-b", "main")
	return repo, git
}

// insertEdit records one AI (or copypaste) edit in the isolated store, the way
// the daemon would, at the given timestamp.
func insertEdit(t *testing.T, repo, file, tool, genType string, tsNanos int64, texts ...string) {
	t.Helper()
	db, err := store.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repoID, _ := gitutil.RepoID(repo)
	if _, err := db.InsertEdit(store.Edit{
		TimestampNanos: tsNanos, RepoPath: repoID, FilePath: file,
		Tool: store.Tool(tool), Confidence: "high", GenType: store.GenType(genType),
		Lines: editLines(texts...),
	}); err != nil {
		t.Fatal(err)
	}
}

// attribute runs the real post-commit sequence on sha and returns the note.
func attribute(t *testing.T, repo, sha string) *Note {
	t.Helper()
	install.RemoveLegacyRepoHooks(repo)
	note, err := AttributeAndWrite(repo, sha)
	if err != nil {
		t.Fatalf("AttributeAndWrite(%s): %v", sha, err)
	}
	return note
}

// addRangeFor returns the add range covering (file, line) in the note, or nil.
func addRangeFor(note *Note, file string, line int) *RangeEntry {
	if note == nil {
		return nil
	}
	for fi := range note.Files {
		if note.Files[fi].Path != file {
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

// lineOf finds the 1-based line number of content in the file at HEAD.
func lineOf(t *testing.T, repo, file, content string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repo, file))
	if err != nil {
		t.Fatal(err)
	}
	for i, l := range strings.Split(string(data), "\n") {
		if l == content {
			return i + 1
		}
	}
	t.Fatalf("line %q not found in %s", content, file)
	return 0
}

// TestAttributeE2E_StashPopCommit is the stash shape: an AI edit is recorded,
// the work is stashed, an UNRELATED commit advances the repo-wide window, the
// stash is popped and committed. The repo-wide bound would exclude the edit;
// the per-file window (last commit touching app.py = c1) keeps it eligible.
func TestAttributeE2E_StashPopCommit(t *testing.T) {
	repo, git := replayE2E(t)
	abs := filepath.Join(repo, "app.py")
	os.WriteFile(abs, []byte("h1\nh2\n"), 0o644)
	other := filepath.Join(repo, "other.txt")
	os.WriteFile(other, []byte("x\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "c1")

	// AI appends ai3 (recorded by the daemon), then the work is stashed.
	insertEdit(t, repo, "app.py", "claude", "chat", time.Now().UnixNano(), "h1", "h2", "ai3")
	os.WriteFile(abs, []byte("h1\nh2\nai3\n"), 0o644)
	git("stash")

	// An unrelated commit lands and is NOTED — this moves the repo-wide
	// PreviousCommitTimestampNanos past the AI edit. The sleep guarantees the
	// edit's timestamp is strictly before c2's (second-precision) commit time.
	time.Sleep(1100 * time.Millisecond)
	os.WriteFile(other, []byte("x\ny\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "c2-unrelated")
	attribute(t, repo, git("rev-parse", "HEAD"))

	// Pop and commit the stashed AI work.
	git("stash", "pop")
	git("add", ".")
	git("commit", "-q", "-m", "c3-popped")
	note := attribute(t, repo, git("rev-parse", "HEAD"))

	r := addRangeFor(note, "app.py", 3)
	if r == nil || r.AuthorType != "AI" || r.Tool != "claude" {
		t.Errorf("stash-popped line: want AI/claude via per-file window, got %+v", r)
	}
}

// TestAttributeE2E_StashPop_PoisonedWorkingLog: same shape, but the editor also
// (wrongly) recorded the popped content as Human in the working log — the
// reconcile is Human→AI only, so a poisoned log cannot block recovery.
func TestAttributeE2E_StashPop_PoisonedWorkingLog(t *testing.T) {
	repo, git := replayE2E(t)
	abs := filepath.Join(repo, "app.py")
	os.WriteFile(abs, []byte("h1\nh2\n"), 0o644)
	other := filepath.Join(repo, "other.txt")
	os.WriteFile(other, []byte("x\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "c1")

	insertEdit(t, repo, "app.py", "claude", "chat", time.Now().UnixNano(), "h1", "h2", "ai3")
	os.WriteFile(abs, []byte("h1\nh2\nai3\n"), 0o644)
	git("stash")

	time.Sleep(1100 * time.Millisecond)
	os.WriteFile(other, []byte("x\ny\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "c2-unrelated")
	attribute(t, repo, git("rev-parse", "HEAD"))
	parent := git("rev-parse", "HEAD")

	git("stash", "pop")
	// The plugin mis-classifies the pop as fresh HUMAN typing in the working log.
	if _, err := authorship.Update(repo, "main", parent, "app.py", "h1\nh2\nai3\n", "h1\nh2\n",
		authorship.Author{Type: authorship.Human, GenType: "human"}, 1); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "c3-popped")
	note := attribute(t, repo, git("rev-parse", "HEAD"))

	r := addRangeFor(note, "app.py", 3)
	if r == nil || r.AuthorType != "AI" || r.Tool != "claude" {
		t.Errorf("poisoned-log stash pop: want AI/claude recovered, got %+v", r)
	}
}

// TestAttributeE2E_StashPop_CopyPastePinRespected: a copypaste record for the
// SAME content inside the widened window must keep the line Human (tagged),
// proving the widened window preserves matcher precedence.
func TestAttributeE2E_StashPop_CopyPastePinRespected(t *testing.T) {
	repo, git := replayE2E(t)
	abs := filepath.Join(repo, "app.py")
	os.WriteFile(abs, []byte("h1\nh2\n"), 0o644)
	other := filepath.Join(repo, "other.txt")
	os.WriteFile(other, []byte("x\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "c1")

	// The human PASTED the line (recorded copypaste); an unrelated AI edit with
	// the same content also exists — copypaste must win in the widened window.
	now := time.Now().UnixNano()
	insertEdit(t, repo, "app.py", "claude", "chat", now-1, "h1", "h2", "pasted3")
	insertEdit(t, repo, "app.py", "copypaste", "human", now, "h1", "h2", "pasted3")
	os.WriteFile(abs, []byte("h1\nh2\npasted3\n"), 0o644)
	git("stash")

	time.Sleep(1100 * time.Millisecond)
	os.WriteFile(other, []byte("x\ny\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "c2-unrelated")
	attribute(t, repo, git("rev-parse", "HEAD"))

	git("stash", "pop")
	git("add", ".")
	git("commit", "-q", "-m", "c3-popped")
	note := attribute(t, repo, git("rev-parse", "HEAD"))

	r := addRangeFor(note, "app.py", 3)
	if r == nil || r.AuthorType != "Human" {
		t.Errorf("pasted line after stash pop: want Human (copypaste pin), got %+v", r)
	}
}

// TestAttributeE2E_PerFileWindow_NoFalsePositiveRetype: once AI content is
// COMMITTED, a human re-adding the identical line later must stay Human — the
// per-file window opens at the commit that landed the AI content, excluding
// the original edit.
func TestAttributeE2E_PerFileWindow_NoFalsePositiveRetype(t *testing.T) {
	repo, git := replayE2E(t)
	abs := filepath.Join(repo, "app.py")
	os.WriteFile(abs, []byte("h1\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "c1")

	// AI adds a line; committed and noted in c2.
	insertEdit(t, repo, "app.py", "claude", "chat", time.Now().UnixNano(), "h1", "ai2")
	os.WriteFile(abs, []byte("h1\nai2\n"), 0o644)
	time.Sleep(1100 * time.Millisecond)
	git("add", ".")
	git("commit", "-q", "-m", "c2")
	note := attribute(t, repo, git("rev-parse", "HEAD"))
	if r := addRangeFor(note, "app.py", 2); r == nil || r.AuthorType != "AI" {
		t.Fatalf("setup: c2 line 2 should be AI, got %+v", r)
	}

	// The human RETYPES the identical content as a new line in c3.
	time.Sleep(1100 * time.Millisecond)
	os.WriteFile(abs, []byte("h1\nai2\nai2\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "c3-retype")
	note = attribute(t, repo, git("rev-parse", "HEAD"))

	if r := addRangeFor(note, "app.py", 3); r == nil || r.AuthorType != "Human" {
		t.Errorf("retyped committed-AI content: want Human (window bounded by c2), got %+v", r)
	}
}

// TestAttributeE2E_CherryPick_Clean: the marker-driven path. The source commit
// is noted on the feature branch; main moves past the edit window; the pick is
// attributed WITH CHERRY_PICK_HEAD present (as at real hook time — pinned by
// the Phase 0 test). Regression for the "skip leaves no note" hole + carry-over.
func TestAttributeE2E_CherryPick_Clean(t *testing.T) {
	repo, git := replayE2E(t)
	abs := filepath.Join(repo, "app.py")
	os.WriteFile(abs, []byte("l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "c1")

	// Feature: AI appends a line; committed + noted there.
	git("checkout", "-q", "-b", "feature")
	insertEdit(t, repo, "app.py", "claude", "chat", time.Now().UnixNano(),
		"l1", "l2", "l3", "l4", "l5", "l6", "l7", "l8", "ai9")
	os.WriteFile(abs, []byte("l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nai9\n"), 0o644)
	time.Sleep(1100 * time.Millisecond)
	git("add", ".")
	git("commit", "-q", "-m", "feat-ai")
	srcSHA := git("rev-parse", "HEAD")
	srcNote := attribute(t, repo, srcSHA)
	if r := addRangeFor(srcNote, "app.py", 9); r == nil || r.AuthorType != "AI" {
		t.Fatalf("setup: source line 9 should be AI, got %+v", r)
	}

	// Main moves — and touches app.py AFTER the edit, so even the per-file
	// window excludes the original edit; only the source NOTE can explain it.
	git("checkout", "-q", "main")
	time.Sleep(1100 * time.Millisecond)
	os.WriteFile(abs, []byte("l1x\nl2\nl3\nl4\nl5\nl6\nl7\nl8\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "main-touch")
	attribute(t, repo, git("rev-parse", "HEAD"))

	git("cherry-pick", srcSHA)
	picked := git("rev-parse", "HEAD")
	// Recreate hook-time state: CHERRY_PICK_HEAD present (Phase 0 pinned this).
	gitDir := git("rev-parse", "--absolute-git-dir")
	if err := os.WriteFile(filepath.Join(gitDir, "CHERRY_PICK_HEAD"), []byte(srcSHA+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(filepath.Join(gitDir, "CHERRY_PICK_HEAD"))

	note := attribute(t, repo, picked)
	if note == nil {
		t.Fatal("cherry-picked commit got NO note (the skip hole) — attribution must run")
	}
	ln := lineOf(t, repo, "app.py", "ai9")
	if r := addRangeFor(note, "app.py", ln); r == nil || r.AuthorType != "AI" || r.Tool != "claude" {
		t.Errorf("cherry-picked AI line: want AI/claude from source note, got %+v", r)
	}
}

// TestAttributeE2E_CherryPick_XTrailer: marker already gone (post-op reality);
// the `-x` trailer identifies the source commit.
func TestAttributeE2E_CherryPick_XTrailer(t *testing.T) {
	repo, git := replayE2E(t)
	abs := filepath.Join(repo, "app.py")
	os.WriteFile(abs, []byte("l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "c1")

	git("checkout", "-q", "-b", "feature")
	insertEdit(t, repo, "app.py", "cursor", "chat", time.Now().UnixNano(),
		"l1", "l2", "l3", "l4", "l5", "l6", "l7", "l8", "ai9")
	os.WriteFile(abs, []byte("l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nai9\n"), 0o644)
	time.Sleep(1100 * time.Millisecond)
	git("add", ".")
	git("commit", "-q", "-m", "feat-ai")
	srcSHA := git("rev-parse", "HEAD")
	attribute(t, repo, srcSHA)

	git("checkout", "-q", "main")
	time.Sleep(1100 * time.Millisecond)
	os.WriteFile(abs, []byte("l1x\nl2\nl3\nl4\nl5\nl6\nl7\nl8\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "main-touch")
	attribute(t, repo, git("rev-parse", "HEAD"))

	git("cherry-pick", "-x", srcSHA)
	note := attribute(t, repo, git("rev-parse", "HEAD"))
	ln := lineOf(t, repo, "app.py", "ai9")
	if r := addRangeFor(note, "app.py", ln); r == nil || r.AuthorType != "AI" || r.Tool != "cursor" {
		t.Errorf("cherry-pick -x AI line: want AI/cursor via trailer, got %+v", r)
	}
}

// TestAttributeE2E_CherryPick_NoSourceNote: the source commit was never noted
// (blamely installed but hook missed it). The gated EditsForFileAny fallback
// recovers from the DB; and with NO db edit for the content, the line stays
// Human — no over-attribution.
func TestAttributeE2E_CherryPick_NoSourceNote(t *testing.T) {
	repo, git := replayE2E(t)
	abs := filepath.Join(repo, "app.py")
	os.WriteFile(abs, []byte("l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "c1")

	git("checkout", "-q", "-b", "feature")
	insertEdit(t, repo, "app.py", "codex", "cli", time.Now().UnixNano(),
		"l1", "l2", "l3", "l4", "l5", "l6", "l7", "l8", "ai9")
	os.WriteFile(abs, []byte("l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nai9\nhuman10\n"), 0o644)
	time.Sleep(1100 * time.Millisecond)
	git("add", ".")
	git("commit", "-q", "-m", "feat-mixed") // NOT attributed — no note
	srcSHA := git("rev-parse", "HEAD")

	git("checkout", "-q", "main")
	time.Sleep(1100 * time.Millisecond)
	os.WriteFile(abs, []byte("l1x\nl2\nl3\nl4\nl5\nl6\nl7\nl8\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "main-touch")
	attribute(t, repo, git("rev-parse", "HEAD"))

	git("cherry-pick", "-x", srcSHA)
	note := attribute(t, repo, git("rev-parse", "HEAD"))
	aiLn := lineOf(t, repo, "app.py", "ai9")
	humanLn := lineOf(t, repo, "app.py", "human10")
	if r := addRangeFor(note, "app.py", aiLn); r == nil || r.AuthorType != "AI" || r.Tool != "codex" {
		t.Errorf("no-source-note pick, AI line: want AI/codex via DB fallback, got %+v", r)
	}
	if r := addRangeFor(note, "app.py", humanLn); r == nil || r.AuthorType != "Human" {
		t.Errorf("no-source-note pick, human line: want Human (no over-attribution), got %+v", r)
	}
}

// TestAttributeE2E_MergeSquash_DefaultMessage: an AI commit + a human commit on
// a feature branch, squashed onto main with the default message. Per-line union
// must hold: the AI line stays AI, the human line stays Human.
func TestAttributeE2E_MergeSquash_DefaultMessage(t *testing.T) {
	repo, git := replayE2E(t)
	abs := filepath.Join(repo, "app.py")
	os.WriteFile(abs, []byte("l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "c1")

	git("checkout", "-q", "-b", "feature")
	insertEdit(t, repo, "app.py", "claude", "chat", time.Now().UnixNano(),
		"l1", "l2", "l3", "l4", "l5", "l6", "l7", "l8", "ai9")
	os.WriteFile(abs, []byte("l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nai9\n"), 0o644)
	time.Sleep(1100 * time.Millisecond)
	git("add", ".")
	git("commit", "-q", "-m", "feat-ai")
	attribute(t, repo, git("rev-parse", "HEAD"))
	// Second (human) commit on the feature branch.
	os.WriteFile(abs, []byte("l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nai9\nhuman10\n"), 0o644)
	time.Sleep(1100 * time.Millisecond)
	git("add", ".")
	git("commit", "-q", "-m", "feat-human")
	attribute(t, repo, git("rev-parse", "HEAD"))

	git("checkout", "-q", "main")
	time.Sleep(1100 * time.Millisecond)
	os.WriteFile(abs, []byte("l1x\nl2\nl3\nl4\nl5\nl6\nl7\nl8\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "main-touch")
	attribute(t, repo, git("rev-parse", "HEAD"))

	git("merge", "--squash", "feature")
	git("commit", "-q", "--no-edit") // default "Squashed commit of the following:" message
	note := attribute(t, repo, git("rev-parse", "HEAD"))

	aiLn := lineOf(t, repo, "app.py", "ai9")
	humanLn := lineOf(t, repo, "app.py", "human10")
	if r := addRangeFor(note, "app.py", aiLn); r == nil || r.AuthorType != "AI" || r.Tool != "claude" {
		t.Errorf("squash AI line: want AI/claude from source notes, got %+v", r)
	}
	if r := addRangeFor(note, "app.py", humanLn); r == nil || r.AuthorType != "Human" {
		t.Errorf("squash human line: want Human, got %+v", r)
	}
}

// TestAttributeE2E_MergeSquash_HumanOnly_NoFalseAI: squashing purely-human
// commits, with an unrelated AI edit in the DB for DIFFERENT content — nothing
// may flip to AI.
func TestAttributeE2E_MergeSquash_HumanOnly_NoFalseAI(t *testing.T) {
	repo, git := replayE2E(t)
	abs := filepath.Join(repo, "app.py")
	os.WriteFile(abs, []byte("l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "c1")

	// Unrelated AI edit (content that never lands in the squash).
	insertEdit(t, repo, "app.py", "claude", "chat", time.Now().UnixNano(), "unrelated-ai-content")

	git("checkout", "-q", "-b", "feature")
	os.WriteFile(abs, []byte("l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nhuman9\n"), 0o644)
	time.Sleep(1100 * time.Millisecond)
	git("add", ".")
	git("commit", "-q", "-m", "feat-human")
	attribute(t, repo, git("rev-parse", "HEAD"))

	git("checkout", "-q", "main")
	git("merge", "--squash", "feature")
	git("commit", "-q", "--no-edit")
	note := attribute(t, repo, git("rev-parse", "HEAD"))

	ln := lineOf(t, repo, "app.py", "human9")
	if r := addRangeFor(note, "app.py", ln); r == nil || r.AuthorType != "Human" {
		t.Errorf("human-only squash: want Human, got %+v", r)
	}
}

// ── Unit: message parsers ──────────────────────────────────────────────────────

func TestReplayFromMessage(t *testing.T) {
	shaA := strings.Repeat("a", 40)
	shaB := strings.Repeat("b", 40)
	cases := []struct {
		name string
		msg  string
		kind replayKind
		srcs []string
	}{
		{"empty", "", replayNone, nil},
		{"plain", "feat: add thing\n", replayNone, nil},
		{"x trailer", "feat: add thing\n\n(cherry picked from commit " + shaA + ")\n", replayCherryPick, []string{shaA}},
		{"two picks", "batch\n\n(cherry picked from commit " + shaA + ")\n(cherry picked from commit " + shaB + ")\n",
			replayCherryPick, []string{shaA, shaB}},
		{"squash default", "Squashed commit of the following:\n\ncommit " + shaA + "\nAuthor: t\n\n    feat-1\n\ncommit " + shaB + "\nAuthor: t\n\n    feat-2\n",
			replaySquashMerge, []string{shaA, shaB}},
		{"squash phrase without commits", "Squashed commit of the following:\n\n(nothing)\n", replayNone, nil},
		{"rewritten squash message", "merge feature work\n", replayNone, nil},
		{"short sha not matched", "(cherry picked from commit abc123)\n", replayNone, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := replayFromMessage(c.msg)
			if got.Kind != c.kind {
				t.Fatalf("kind = %v, want %v", got.Kind, c.kind)
			}
			if len(got.SourceSHAs) != len(c.srcs) {
				t.Fatalf("srcs = %v, want %v", got.SourceSHAs, c.srcs)
			}
			for i := range c.srcs {
				if got.SourceSHAs[i] != c.srcs[i] {
					t.Errorf("src[%d] = %s, want %s", i, got.SourceSHAs[i], c.srcs[i])
				}
			}
		})
	}
}

// TestSourceNoteIndex_ConsumeOnce: duplicate content across two source commits
// yields exactly two budget units — a third identical committed line stays
// unmatched.
func TestSourceNoteIndex_ConsumeOnce(t *testing.T) {
	ix := &sourceNoteIndex{byFile: map[string][]*sourceAttrUnit{
		"f.go": {
			{sha: sha256HexStr([]byte("dup")), norm: sha256HexNormStr("dup"), tool: "claude"},
			{sha: sha256HexStr([]byte("dup")), norm: sha256HexNormStr("dup"), tool: "cursor"},
		},
	}}
	if u, ok := ix.match("f.go", "dup"); !ok || u.tool != "claude" {
		t.Fatalf("first match: want claude, got %+v ok=%v", u, ok)
	}
	if u, ok := ix.match("f.go", "dup"); !ok || u.tool != "cursor" {
		t.Fatalf("second match: want cursor, got %+v ok=%v", u, ok)
	}
	if _, ok := ix.match("f.go", "dup"); ok {
		t.Fatal("third match: budget must be exhausted")
	}
	if _, ok := ix.match("f.go", "   "); ok {
		t.Fatal("blank content must never match")
	}
}
