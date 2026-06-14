package tools

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// ── Pure parsers ──────────────────────────────────────────────────────────────

func TestDeletePathFromInput(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"cursor path field", `{"path":"/repo/login.html"}`, "/repo/login.html"},
		{"claude file_path field", `{"file_path":"/repo/a.go"}`, "/repo/a.go"},
		{"path preferred over file_path", `{"path":"/p","file_path":"/f"}`, "/p"},
		{"neither", `{"other":1}`, ""},
		{"malformed", `not json`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := deletePathFromInput(json.RawMessage(c.in)); got != c.want {
				t.Errorf("deletePathFromInput(%s) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestCursorGenType(t *testing.T) {
	cases := []struct {
		name string
		p    claudeHookPayload
		want string
	}{
		{"explicit gen_type wins", claudeHookPayload{GenType: "completion", SessionID: "s"}, "completion"},
		{"no conversation context → tab completion", claudeHookPayload{}, "completion"},
		{"session present → chat", claudeHookPayload{SessionID: "s"}, "chat"},
		{"conversation id present → chat", claudeHookPayload{ConversationID: "c"}, "chat"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := cursorGenType(c.p); got != c.want {
				t.Errorf("cursorGenType = %q, want %q", got, c.want)
			}
		})
	}
}

func TestParsePatchDeletedFiles(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "canonical input envelope, single delete",
			in:   `{"input":"*** Begin Patch\n*** Delete File: src/old.go\n*** End Patch"}`,
			want: []string{"src/old.go"},
		},
		{
			name: "mixed add + delete in one patch",
			in:   `{"input":"*** Begin Patch\n*** Add File: a.go\n+package a\n*** Delete File: b.go\n*** End Patch"}`,
			want: []string{"b.go"},
		},
		{
			name: "bare patch body (not a JSON object)",
			in:   `"*** Begin Patch\n*** Delete File: gone.txt\n*** End Patch"`,
			want: []string{"gone.txt"},
		},
		{
			name: "no deletions",
			in:   `{"input":"*** Begin Patch\n*** Update File: keep.go\n+x\n*** End Patch"}`,
			want: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parsePatchDeletedFiles(json.RawMessage(c.in))
			if len(got) != len(c.want) {
				t.Fatalf("parsePatchDeletedFiles = %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], c.want[i])
				}
			}
		})
	}
}

// ── Git-backed deletion fingerprinting ──────────────────────────────────────

// initRepoWithFile creates a git repo containing `rel` with `content`, commits
// it, then removes it from the working tree (simulating an AI delete tool that
// has run but not yet committed). Returns the repo root.
func initRepoWithFile(t *testing.T, rel, content string) string {
	t.Helper()
	root := t.TempDir()
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
	// Pin line-ending handling so the same content hashes identically on every
	// OS — on Windows git defaults to core.autocrlf=true, which would rewrite
	// the blob and break content_sha-based assertions.
	run("config", "core.autocrlf", "false")
	abs := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "add file")
	if err := os.Remove(abs); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestHeadFileRemovedHashes_FingerprintsDeletedFile(t *testing.T) {
	root := initRepoWithFile(t, "login.html", "line one\nline two\n\nline four\n")
	hashes := headFileRemovedHashes(root, "login.html")
	// 3 non-blank lines fingerprinted (the blank line carries no content_sha).
	if len(hashes) != 3 {
		t.Fatalf("got %d hashes, want 3 (non-blank lines)", len(hashes))
	}
	for _, h := range hashes {
		if h.ContentSHA == "" {
			t.Errorf("expected a content_sha for every removed line, got empty")
		}
	}
}

func TestBuildHeadDeletionPayload_DeletedAtHead(t *testing.T) {
	root := initRepoWithFile(t, "login.html", "alpha\nbeta\ngamma\n")
	payload, ok := buildHeadDeletionPayload(root, "login.html", "cursor", "chat", "claude-x", "sess-1", "/tp", "cursor_delete")
	if !ok {
		t.Fatal("expected ok=true for a file present at HEAD")
	}
	if payload.Tool != "cursor" {
		t.Errorf("Tool = %q, want cursor", payload.Tool)
	}
	if payload.GenType != "chat" {
		t.Errorf("GenType = %q, want chat", payload.GenType)
	}
	if payload.FilePath != "login.html" {
		t.Errorf("FilePath = %q, want login.html", payload.FilePath)
	}
	if payload.Model != "claude-x" {
		t.Errorf("Model = %q, want claude-x", payload.Model)
	}
	if len(payload.RemovedLines) != 3 {
		t.Errorf("RemovedLines = %d, want 3", len(payload.RemovedLines))
	}
	if len(payload.Lines) != 0 {
		t.Errorf("a pure deletion must record no added Lines, got %d", len(payload.Lines))
	}
	if payload.SuggestedLines != 3 {
		t.Errorf("SuggestedLines = %d, want 3", payload.SuggestedLines)
	}
}

func TestBuildHeadDeletionPayload_NotAtHead(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command("git", "-C", root, "init")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	// File never existed at HEAD → nothing to fingerprint.
	if _, ok := buildHeadDeletionPayload(root, "ghost.txt", "cursor", "chat", "", "", "", "x"); ok {
		t.Error("expected ok=false when the file isn't at HEAD")
	}
}

// TestRecordToolDeletionPath_ResolvesRelFromGoneFile exercises the absolute-path
// entry point used by Cursor's `Delete` (and Codex's delete-file) with a path
// that no longer exists on disk: the repo-relative name must still resolve from
// the surviving parent directory. We assert via the payload builder the same
// rel the recorder would use.
func TestRecordToolDeletionPath_ResolvesRelFromGoneFile(t *testing.T) {
	root := initRepoWithFile(t, "pages/login.html", "x\ny\n")
	abs := filepath.Join(root, "pages", "login.html")
	// recordToolDeletionPath posts to the (absent) daemon and returns nil; the
	// meaningful assertion is that the same resolution yields the right payload.
	if err := recordToolDeletionPath(abs, root, "cursor", "chat", "", "", "", "cursor_delete"); err != nil {
		t.Fatalf("recordToolDeletionPath: %v", err)
	}
	payload, ok := buildHeadDeletionPayload(root, "pages/login.html", "cursor", "chat", "", "", "", "cursor_delete")
	if !ok || payload.FilePath != "pages/login.html" || len(payload.RemovedLines) != 2 {
		t.Fatalf("unexpected payload ok=%v file=%q removed=%d", ok, payload.FilePath, len(payload.RemovedLines))
	}
}

// TestRecordShellDeletions_OnlyCommandTargets reproduces the over-attribution
// bug: the AI ran `rm register.html`, but the user had ALSO deleted contact.html
// by hand. Both are gone from the tree, but only register.html (the command's
// target) may be credited to the AI — contact.html must be left for Human.
func TestRecordShellDeletions_OnlyCommandTargets(t *testing.T) {
	root := t.TempDir()
	run := func(a ...string) {
		cmd := exec.Command("git", append([]string{"-C", root}, a...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", a, err, out)
		}
	}
	run("init")
	run("config", "core.autocrlf", "false") // stable hashes across OSes
	if err := os.WriteFile(filepath.Join(root, "register.html"), []byte("r1\nr2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "contact.html"), []byte("c1\nc2\nc3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "add both files")
	// Both are now removed from the working tree (uncommitted): register.html by
	// the AI's `rm`, contact.html by the human — git reports both as deleted.
	_ = os.Remove(filepath.Join(root, "register.html"))
	_ = os.Remove(filepath.Join(root, "contact.html"))

	// The AI command only named register.html.
	targets := shellDeleteTargets("rm " + filepath.Join(root, "register.html") + " && echo deleted")
	credited := map[string]bool{}
	for _, rel := range gitDeletedFiles(root) {
		if MatchesFileOp(rel, targets) {
			credited[rel] = true
		}
	}
	if !credited["register.html"] {
		t.Error("register.html (the rm target) should be credited to the AI")
	}
	if credited["contact.html"] {
		t.Error("contact.html (deleted by the human) must NOT be credited to the AI")
	}
}

func TestShellCommandFromInput(t *testing.T) {
	cases := map[string]string{
		`{"command":"rm a.html"}`:   "rm a.html",
		`{"cmd":"del b.html"}`:      "del b.html",
		`{"command":"x","cmd":"y"}`: "x", // command preferred
		`{"other":1}`:               "",
		`not json`:                  "",
	}
	for in, want := range cases {
		if got := shellCommandFromInput(json.RawMessage(in)); got != want {
			t.Errorf("shellCommandFromInput(%s) = %q, want %q", in, got, want)
		}
	}
}
