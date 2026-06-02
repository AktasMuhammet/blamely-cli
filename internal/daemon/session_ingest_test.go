package daemon

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/blamely/blamely/internal/store"
)

// TestValidateAndStore_ResolvesBranchSession exercises the full ingest path
// against a real git repo: an edit posted while on a feature branch must come
// back tagged with that branch and a non-null session id.
func TestValidateAndStore_ResolvesBranchSession(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	git("commit", "--allow-empty", "-q", "-m", "init")
	git("checkout", "-q", "-b", "feature/x")

	db := openTestDB(t)
	p := EditPayload{
		Tool:     "copilot",
		GenType:  "chat",
		RepoPath: repo,
		FilePath: "a.go",
		Lines:    []Range{{Start: 1, End: 1, ContentSHA: "abc"}},
		// Branch intentionally omitted → daemon resolves it from the repo.
	}
	if err := validateAndStore(db, p); err != nil {
		t.Fatalf("validateAndStore: %v", err)
	}

	edits, err := db.EditsForFileAny(repo, "a.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	e := edits[0]
	if e.Branch != "feature/x" {
		t.Errorf("branch: want feature/x, got %q", e.Branch)
	}
	if !e.SessionID.Valid || e.SessionID.String == "" {
		t.Errorf("expected a non-null UUID session_id")
	}

	// The session row must exist and be re-resolvable to the same id.
	id, err := db.ResolveSession(repo, "feature/x", sessionBaseFor(t, repo))
	if err != nil {
		t.Fatal(err)
	}
	if id != e.SessionID.String {
		t.Errorf("session id mismatch: edit=%q resolve=%q", e.SessionID.String, id)
	}
	_ = store.GenTypeChat // keep store import meaningful if asserts change
}

// sessionBaseFor mirrors how the resolver computes base_sha (current HEAD).
func sessionBaseFor(t *testing.T, repo string) string {
	t.Helper()
	gi := sessions.gitInfo(repo)
	return gi.baseSha
}

// TestWorkSessionClosesOnCommit verifies one session per branch per HEAD tip:
// edits before a commit use the pre-commit HEAD; edits after use a new session.
func TestWorkSessionClosesOnCommit(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	git("commit", "--allow-empty", "-q", "-m", "init")
	headBefore := strings.TrimSpace(mustGit(t, repo, "rev-parse", "HEAD"))

	db := openTestDB(t)
	InvalidateSessionCache(repo)
	if err := validateAndStore(db, EditPayload{
		Tool: "copilot", GenType: "chat", RepoPath: repo, FilePath: "a.go",
		Lines: []Range{{Start: 1, End: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	edits, _ := db.EditsForFileAny(repo, "a.go")
	if len(edits) != 1 || !edits[0].SessionID.Valid {
		t.Fatal("expected session on first edit")
	}
	sid1 := edits[0].SessionID.String

	git("commit", "--allow-empty", "-q", "-m", "second")
	InvalidateSessionCache(repo)
	headAfter := strings.TrimSpace(mustGit(t, repo, "rev-parse", "HEAD"))
	if headAfter == headBefore {
		t.Fatal("HEAD should advance after commit")
	}

	if err := validateAndStore(db, EditPayload{
		Tool: "copilot", GenType: "chat", RepoPath: repo, FilePath: "b.go",
		Lines: []Range{{Start: 1, End: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	edits2, _ := db.EditsForFileAny(repo, "b.go")
	if len(edits2) != 1 || !edits2[0].SessionID.Valid {
		t.Fatal("expected session on post-commit edit")
	}
	if edits2[0].SessionID.String == sid1 {
		t.Fatalf("post-commit edit should be a new session, still %q", sid1)
	}
}

func mustGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}
