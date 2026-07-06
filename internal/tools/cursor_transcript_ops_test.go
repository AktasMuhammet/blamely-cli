package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pinCursorHome makes CursorCommitFileOps read `home` as the user home for the rest
// of the test — hermetic, independent of the process-global HOME env that sibling
// tests in this package mutate via t.Setenv. Restored on cleanup.
func pinCursorHome(t *testing.T, home string) {
	t.Helper()
	prev := cursorHomeDir
	cursorHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { cursorHomeDir = prev })
}

// writeCursorTranscript lays out a Cursor agent transcript for repoRoot under a
// fake HOME and returns the repoRoot. Lines are the real Cursor shape:
// {"role":"assistant","message":{"content":[{"type":"tool_use",...}]}}.
func writeCursorTranscript(t *testing.T, home, repoRoot string, lines ...string) {
	t.Helper()
	pinCursorHome(t, home)
	proj := strings.ReplaceAll(strings.TrimPrefix(filepath.ToSlash(repoRoot), "/"), "/", "-")
	dir := filepath.Join(home, ".cursor", "projects", proj, "agent-transcripts", "sess-uuid")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sess-uuid.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCursorCommitFileOps(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// repoRoot must be absolute so the leading-slash encoding matches Cursor's.
	repoRoot := "/Users/dev/workspace/proj"
	abs := repoRoot + "/src/gone.ts"

	writeCursorTranscript(t, home, repoRoot,
		// git rm through the Shell tool — the case that used to fall to Human.
		`{"role":"assistant","message":{"content":[{"type":"tool_use","name":"Shell","input":{"command":"git rm simple-register.html && git commit -m x"}}]}}`,
		// plain rm with a quoted space.
		`{"role":"assistant","message":{"content":[{"type":"tool_use","name":"Shell","input":{"command":"rm 'login page.html'"}}]}}`,
		// structured Delete tool with an absolute path -> repo-relative.
		`{"role":"assistant","message":{"content":[{"type":"tool_use","name":"Delete","input":{"path":"`+abs+`"}}]}}`,
		// whole-file Write.
		`{"role":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"path":"`+repoRoot+`/new.ts"}}]}}`,
		// heredoc redirect create.
		`{"role":"assistant","message":{"content":[{"type":"text","text":"noise"},{"type":"tool_use","name":"Shell","input":{"command":"cat > out.txt <<'EOF'\nhi\nEOF"}}]}}`,
	)

	written, deleted := CursorCommitFileOps(repoRoot, 0)

	if !MatchesFileOp("simple-register.html", deleted) {
		t.Errorf("git rm target not caught; deleted=%v", deleted)
	}
	if !MatchesFileOp("login page.html", deleted) {
		t.Errorf("quoted-space rm target not caught; deleted=%v", deleted)
	}
	if !eq(deleted, []string{"simple-register.html", "login page.html", "src/gone.ts"}) {
		t.Errorf("deleted = %v", deleted)
	}
	if !eq(written, []string{"new.ts", "out.txt"}) {
		t.Errorf("written = %v", written)
	}
}

// On Windows the on-disk project folder is encoded from the editor's uppercase-
// drive cwd, but git's toplevel (what the backfill receives) uses a lowercase
// drive — so the folder name differs only in case. Resolution must still find it.
func TestCursorCommitFileOps_CaseInsensitiveDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Lay the transcript down under an UPPER-case name...
	writeCursorTranscript(t, home, "/Users/DEV/Proj",
		`{"role":"assistant","message":{"content":[{"type":"tool_use","name":"Shell","input":{"command":"git rm gone.html"}}]}}`,
	)
	// ...but query with a differently-cased repo root, as git would produce it.
	_, deleted := CursorCommitFileOps("/users/dev/proj", 0)
	if !MatchesFileOp("gone.html", deleted) {
		t.Errorf("case-insensitive project dir not resolved; deleted=%v", deleted)
	}
}

// Windows folders can't contain ':', so Cursor substitutes the drive colon
// (C:\dev\proj -> "C--dev-proj"), while git reports "c:/dev/proj" -> "c:-dev-proj".
// The drive-stripped tail ("dev-proj") is what matches.
func TestCursorCommitFileOps_WindowsDriveColonSubstitution(t *testing.T) {
	home := t.TempDir()
	proj := "C--dev-proj" // colon replaced by '-', as Windows requires
	dir := filepath.Join(home, ".cursor", "projects", proj, "agent-transcripts", "sess")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"role":"assistant","message":{"content":[{"type":"tool_use","name":"Shell","input":{"command":"git rm gone.html"}}]}}`
	if err := os.WriteFile(filepath.Join(dir, "sess.jsonl"), []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	pinCursorHome(t, home)

	// git-style repo root: lowercase drive, forward slashes -> encodes to "c:-dev-proj".
	_, deleted := CursorCommitFileOps("C:/dev/proj", 0)
	if !MatchesFileOp("gone.html", deleted) {
		t.Errorf("windows colon-substituted dir not resolved; deleted=%v", deleted)
	}
}

func TestStripDrivePrefix(t *testing.T) {
	cases := map[string]string{
		"C:-Users-dev-proj": "Users-dev-proj",
		"C--Users-dev-proj": "Users-dev-proj",
		"c:-dev-proj":       "dev-proj",
		"Users-dev-proj":    "Users-dev-proj", // no drive -> unchanged (macOS/Linux)
		"src-main":          "src-main",
	}
	for in, want := range cases {
		if got := stripDrivePrefix(in); got != want {
			t.Errorf("stripDrivePrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCursorCommitFileOps_MtimeWindow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoRoot := "/Users/dev/workspace/proj"
	writeCursorTranscript(t, home, repoRoot,
		`{"role":"assistant","message":{"content":[{"type":"tool_use","name":"Shell","input":{"command":"git rm old.html"}}]}}`,
	)
	// A since far in the future excludes the transcript by mtime.
	_, deleted := CursorCommitFileOps(repoRoot, 1<<62)
	if len(deleted) != 0 {
		t.Errorf("expected mtime window to exclude stale transcript, got %v", deleted)
	}
}
