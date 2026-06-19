package gitnotes

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/blamely/blamely/internal/store"
)

func gitInitCommit(t *testing.T, content string) (repo string) {
	t.Helper()
	repo = t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "f.txt")
	run("commit", "-m", "c")
	return repo
}

// TestSynthesizeWriteRemovals_CreditsMatchingWrite reproduces commit fe8e9ff: an
// AI whole-file Write recorded the committed content but no removed lines (stale
// snapshot). The committed file == the edit's content, so the file's deletions
// must be credited to that edit's tool.
func TestSynthesizeWriteRemovals_CreditsMatchingWrite(t *testing.T) {
	// Committed state: two keep lines (the cimbom line was removed).
	committed := "keep one\nkeep two\n"
	repo := gitInitCommit(t, committed)

	// An AI Write edit whose recorded content == the committed file.
	writeEdit := store.Edit{
		ID:             42,
		Tool:           store.ToolCursor,
		TimestampNanos: 1000, // in (minNanos, maxNanos] below
		Lines: []store.EditLine{
			{StartLine: 1, EndLine: 1, ContentSHA: sha256HexStr([]byte("keep one"))},
			{StartLine: 2, EndLine: 2, ContentSHA: sha256HexStr([]byte("keep two"))},
		},
	}
	delEdits := []store.Edit{writeEdit}
	delLines := []DeletedLine{{LineNum: 3, Content: "  <p>cimbom</p>"}}

	remSHA, remNorm := removedHashMultisets(delEdits)
	synthesizeWriteRemovals(repo, "HEAD", "f.txt", delEdits, delLines, remSHA, remNorm)

	want := sha256HexStr([]byte("  <p>cimbom</p>"))
	if remSHA[42][want] != 1 {
		t.Fatalf("expected synthesized removal budget 1 for the matching Write, got %d (remSHA=%v)", remSHA[42][want], remSHA)
	}

	// End-to-end: the deletion now attributes to cursor.
	var totals Totals
	var bg ByGenType
	entries := attributeDeletedLines(delLines, delEdits, 0, 1<<62, remSHA, remNorm, &totals, &bg, map[string]*moveAttr{})
	if len(entries) != 1 || entries[0].AuthorType != "AI" || entries[0].Tool != string(store.ToolCursor) {
		t.Fatalf("deletion not attributed to cursor: %+v", entries)
	}
}

// TestSynthesizeWriteRemovals_NoMatchNoCredit: an edit whose content does NOT
// equal the committed file (e.g. a partial/narrowed edit, or a later human edit)
// must not be credited — guards against over-attribution.
func TestSynthesizeWriteRemovals_NoMatchNoCredit(t *testing.T) {
	repo := gitInitCommit(t, "keep one\nkeep two\n")
	partial := store.Edit{
		ID:    7,
		Tool:  store.ToolCursor,
		Lines: []store.EditLine{{StartLine: 1, EndLine: 1, ContentSHA: sha256HexStr([]byte("only this"))}}, // != committed
	}
	delEdits := []store.Edit{partial}
	delLines := []DeletedLine{{LineNum: 3, Content: "  <p>cimbom</p>"}}
	remSHA, remNorm := removedHashMultisets(delEdits)
	synthesizeWriteRemovals(repo, "HEAD", "f.txt", delEdits, delLines, remSHA, remNorm)
	if len(remSHA[7]) != 0 {
		t.Fatalf("non-matching edit must not be credited, got %v", remSHA[7])
	}
}
