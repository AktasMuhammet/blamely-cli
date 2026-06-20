package gitnotes

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/blamely/blamely/internal/config"
)

func TestParseHunkHeader_Normal(t *testing.T) {
	start, length, ok := parseHunkHeader("@@ -1,5 +10,4 @@ func foo()")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if start != 10 {
		t.Errorf("start: want 10, got %d", start)
	}
	if length != 4 {
		t.Errorf("length: want 4, got %d", length)
	}
}

func TestParseHunkHeader_LengthOmitted(t *testing.T) {
	// When length is omitted, it defaults to 1.
	start, length, ok := parseHunkHeader("@@ -1 +5 @@ func bar()")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if start != 5 {
		t.Errorf("start: want 5, got %d", start)
	}
	if length != 1 {
		t.Errorf("length: want 1 (default), got %d", length)
	}
}

func TestParseHunkHeader_ZeroAddedLines(t *testing.T) {
	// "+N,0" means a deletion — no added lines.
	start, length, ok := parseHunkHeader("@@ -1,3 +2,0 @@")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if start != 2 {
		t.Errorf("start: want 2, got %d", start)
	}
	if length != 0 {
		t.Errorf("length: want 0, got %d", length)
	}
}

func TestParseHunkHeader_Malformed(t *testing.T) {
	cases := []string{
		"not a hunk header",
		"@@",
		"@@ @@",
		"@@ -1,2 @@",
	}
	for _, c := range cases {
		_, _, ok := parseHunkHeader(c)
		if ok {
			t.Errorf("parseHunkHeader(%q): want ok=false", c)
		}
	}
}

func TestParseHunkHeader_FirstLine(t *testing.T) {
	// "+1" (new file, first hunk) is the common case for added files.
	start, length, ok := parseHunkHeader("@@ -0,0 +1 @@")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if start != 1 {
		t.Errorf("start: want 1, got %d", start)
	}
	if length != 1 {
		t.Errorf("length: want 1, got %d", length)
	}
}

func TestParseHunkHeaderBothSides_Normal(t *testing.T) {
	delStart, addStart, ok := parseHunkHeaderBothSides("@@ -10,5 +20,3 @@")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if delStart != 10 {
		t.Errorf("delStart: want 10, got %d", delStart)
	}
	if addStart != 20 {
		t.Errorf("addStart: want 20, got %d", addStart)
	}
}

func TestParseHunkHeaderBothSides_LengthsOmitted(t *testing.T) {
	delStart, addStart, ok := parseHunkHeaderBothSides("@@ -7 +9 @@ ctx")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if delStart != 7 || addStart != 9 {
		t.Errorf("want (7,9), got (%d,%d)", delStart, addStart)
	}
}

func TestParseHunkHeaderBothSides_PureDelete(t *testing.T) {
	// "+5,0" — deletion-only hunk; still parseable on both sides.
	delStart, addStart, ok := parseHunkHeaderBothSides("@@ -3,2 +5,0 @@")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if delStart != 3 || addStart != 5 {
		t.Errorf("want (3,5), got (%d,%d)", delStart, addStart)
	}
}

func TestParseHunkHeaderBothSides_Malformed(t *testing.T) {
	for _, c := range []string{"@@", "not a hunk", "@@ +1 @@"} {
		if _, _, ok := parseHunkHeaderBothSides(c); ok {
			t.Errorf("%q: want ok=false", c)
		}
	}
}

// ---- parseDiff: hunk-level pairing of - and + lines ----

// TestParseDiff_InPlaceModification verifies that a 1-for-1 replacement
// (the simplest modification: same pre/post line number) is reported as
// an add only — the matching - line must not appear in Deleted.
func TestParseDiff_InPlaceModification(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/foo.go b/foo.go",
		"--- a/foo.go",
		"+++ b/foo.go",
		"@@ -4,1 +4,1 @@",
		"-count = 1",
		"+count = 2",
		"",
	}, "\n")
	c, err := parseDiff(strings.NewReader(diff), nil)
	if err != nil {
		t.Fatal(err)
	}
	// The two lines are similar (a reworded value), so it's a modification: the
	// add is counted, the paired delete is dropped.
	wantAdded := []AddedLine{{File: "foo.go", LineNum: 4, Content: "count = 2"}}
	if !reflect.DeepEqual(c.Added, wantAdded) {
		t.Errorf("Added: want %v, got %v", wantAdded, c.Added)
	}
	if len(c.Deleted["foo.go"]) != 0 {
		t.Errorf("Deleted: want empty, got %v", c.Deleted["foo.go"])
	}
}

// TestParseDiff_ModificationAfterLineShift is the bug the dedup-by-line-number
// approach got wrong: an earlier hunk inserts lines, shifting the file's net
// line count, then a later hunk replaces a line. The replacement's pre-image
// line number no longer equals its post-image line number, but it's still a
// modification — neither side should be reported as a pure deletion.
func TestParseDiff_ModificationAfterLineShift(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/foo.go b/foo.go",
		"--- a/foo.go",
		"+++ b/foo.go",
		// Insert 2 lines near the top — shifts every later line down by 2.
		"@@ -1,0 +2,2 @@",
		"+new1",
		"+new2",
		// In-place replacement at pre-image line 4 → post-image line 6.
		"@@ -4,1 +6,1 @@",
		"-y = 4",
		"+y = 6",
		"",
	}, "\n")
	c, err := parseDiff(strings.NewReader(diff), nil)
	if err != nil {
		t.Fatal(err)
	}
	wantAdded := []AddedLine{
		{File: "foo.go", LineNum: 2, Content: "new1"},
		{File: "foo.go", LineNum: 3, Content: "new2"},
		{File: "foo.go", LineNum: 6, Content: "y = 6"},
	}
	if !reflect.DeepEqual(c.Added, wantAdded) {
		t.Errorf("Added: want %v, got %v", wantAdded, c.Added)
	}
	if got := c.Deleted["foo.go"]; len(got) != 0 {
		t.Errorf("Deleted: want empty (replacement is a modification), got %v", got)
	}
}

// TestParseDiff_ReplaceMoreWithFewer covers M-to-N replacements where M > N:
// the first N pairs are modifications, the remaining M-N are pure deletes.
func TestParseDiff_ReplaceMoreWithFewer(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/foo.go b/foo.go",
		"--- a/foo.go",
		"+++ b/foo.go",
		// 3 lines removed, 1 added — the add reworks the first del (similar), so
		// that pair is a modification (delete dropped); the other 2 are net deletes.
		"@@ -10,3 +10,1 @@",
		"-value = 1",
		"-removedB",
		"-removedC",
		"+value = 2",
		"",
	}, "\n")
	c, err := parseDiff(strings.NewReader(diff), nil)
	if err != nil {
		t.Fatal(err)
	}
	wantAdded := []AddedLine{{File: "foo.go", LineNum: 10, Content: "value = 2"}}
	if !reflect.DeepEqual(c.Added, wantAdded) {
		t.Errorf("Added: want %v, got %v", wantAdded, c.Added)
	}
	// First del (line 10) is similar to the add → dropped. Lines 11, 12 remain.
	wantDel := []DeletedLine{{LineNum: 11, Content: "removedB"}, {LineNum: 12, Content: "removedC"}}
	if got := c.Deleted["foo.go"]; !reflect.DeepEqual(got, wantDel) {
		t.Errorf("Deleted: want %v, got %v", wantDel, got)
	}
}

// TestParseDiff_ReplaceFewerWithMore is the inverse: 1 line removed, 3 added.
// 1 modification + 2 net adds. No deletions reported.
func TestParseDiff_ReplaceFewerWithMore(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/foo.go b/foo.go",
		"--- a/foo.go",
		"+++ b/foo.go",
		"@@ -10,1 +10,3 @@",
		"-n = 1",
		"+n = 2",
		"+newB",
		"+newC",
		"",
	}, "\n")
	c, err := parseDiff(strings.NewReader(diff), nil)
	if err != nil {
		t.Fatal(err)
	}
	// First del/add pair is a modification (n = 1 → n = 2, similar); the other
	// two adds are net additions. No deletions.
	wantAdded := []AddedLine{
		{File: "foo.go", LineNum: 10, Content: "n = 2"},
		{File: "foo.go", LineNum: 11, Content: "newB"},
		{File: "foo.go", LineNum: 12, Content: "newC"},
	}
	if !reflect.DeepEqual(c.Added, wantAdded) {
		t.Errorf("Added: want %v, got %v", wantAdded, c.Added)
	}
	if got := c.Deleted["foo.go"]; len(got) != 0 {
		t.Errorf("Deleted: want empty, got %v", got)
	}
}

// TestParseDiff_WhitespaceOnlyModification: a reindent (tabs/spaces only) is a
// modification — the two lines are whitespace-normalized-equal, so the add is
// counted and the paired delete is dropped (no phantom deletion).
func TestParseDiff_WhitespaceOnlyModification(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/foo.go b/foo.go",
		"--- a/foo.go",
		"+++ b/foo.go",
		"@@ -7,1 +7,1 @@",
		"-  return x", // 2-space indent
		"+    return x", // 4-space indent — same line, reindented.
		"",
	}, "\n")
	c, err := parseDiff(strings.NewReader(diff), nil)
	if err != nil {
		t.Fatal(err)
	}
	wantAdded := []AddedLine{{File: "foo.go", LineNum: 7, Content: "    return x"}}
	if !reflect.DeepEqual(c.Added, wantAdded) {
		t.Errorf("Added: want %v, got %v", wantAdded, c.Added)
	}
	if got := c.Deleted["foo.go"]; len(got) != 0 {
		t.Errorf("Deleted: want empty (whitespace-only modification), got %v", got)
	}
}

// TestParseDiff_NoNewlineAtEofIsNotAnAdd reproduces "append a line after a file
// whose last line had no trailing newline": git shows the old last line as
// -/+ (it gained a newline) plus the genuinely new line. The re-added,
// byte-identical line must NOT be counted as an addition — only the new line is.
func TestParseDiff_NoNewlineAtEofIsNotAnAdd(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/foo.txt b/foo.txt",
		"--- a/foo.txt",
		"+++ b/foo.txt",
		"@@ -1,1 +1,2 @@",
		"-last line",
		"\\ No newline at end of file",
		"+last line",
		"+a brand new appended line",
		"\\ No newline at end of file",
		"",
	}, "\n")
	c, err := parseDiff(strings.NewReader(diff), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Only the genuinely new line is an add; the re-added identical line is not.
	wantAdded := []AddedLine{{File: "foo.txt", LineNum: 2, Content: "a brand new appended line"}}
	if !reflect.DeepEqual(c.Added, wantAdded) {
		t.Errorf("Added: want %v, got %v", wantAdded, c.Added)
	}
	if got := c.Deleted["foo.txt"]; len(got) != 0 {
		t.Errorf("Deleted: want empty (newline-only change), got %v", got)
	}
}

// TestParseDiff_DissimilarReplaceCountsDeletion is the content-aware pairing
// (option 3): a hunk that deletes a line and adds a COMPLETELY DIFFERENT line at
// the same spot is a real delete + add, not a modification — so the delete is
// reported, matching `git --stat`. Previously the positional pairing silently
// dropped it.
func TestParseDiff_DissimilarReplaceCountsDeletion(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/foo.go b/foo.go",
		"--- a/foo.go",
		"+++ b/foo.go",
		"@@ -3,1 +3,1 @@",
		"-hello1, hello2",
		"+something entirely unrelated here",
		"",
	}, "\n")
	c, err := parseDiff(strings.NewReader(diff), nil)
	if err != nil {
		t.Fatal(err)
	}
	wantAdded := []AddedLine{{File: "foo.go", LineNum: 3, Content: "something entirely unrelated here"}}
	if !reflect.DeepEqual(c.Added, wantAdded) {
		t.Errorf("Added: want %v, got %v", wantAdded, c.Added)
	}
	wantDel := []DeletedLine{{LineNum: 3, Content: "hello1, hello2"}}
	if got := c.Deleted["foo.go"]; !reflect.DeepEqual(got, wantDel) {
		t.Errorf("Deleted: want %v (dissimilar replace), got %v", wantDel, got)
	}
}

// TestParseDiff_PureDeletion confirms that hunks with only - lines (no
// paired +) still produce deletions.
func TestParseDiff_PureDeletion(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/foo.go b/foo.go",
		"--- a/foo.go",
		"+++ b/foo.go",
		"@@ -5,2 +4,0 @@",
		"-gone1",
		"-gone2",
		"",
	}, "\n")
	c, err := parseDiff(strings.NewReader(diff), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Added) != 0 {
		t.Errorf("Added: want empty, got %v", c.Added)
	}
	wantDel := []DeletedLine{{LineNum: 5, Content: "gone1"}, {LineNum: 6, Content: "gone2"}}
	if got := c.Deleted["foo.go"]; !reflect.DeepEqual(got, wantDel) {
		t.Errorf("Deleted: want %v, got %v", wantDel, got)
	}
}

// TestParseDiff_PureInsertion confirms that hunks with only + lines (no
// paired -) still produce adds.
func TestParseDiff_PureInsertion(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/foo.go b/foo.go",
		"--- a/foo.go",
		"+++ b/foo.go",
		"@@ -3,0 +4,2 @@",
		"+inserted1",
		"+inserted2",
		"",
	}, "\n")
	c, err := parseDiff(strings.NewReader(diff), nil)
	if err != nil {
		t.Fatal(err)
	}
	wantAdded := []AddedLine{
		{File: "foo.go", LineNum: 4, Content: "inserted1"},
		{File: "foo.go", LineNum: 5, Content: "inserted2"},
	}
	if !reflect.DeepEqual(c.Added, wantAdded) {
		t.Errorf("Added: want %v, got %v", wantAdded, c.Added)
	}
	if got := c.Deleted["foo.go"]; len(got) != 0 {
		t.Errorf("Deleted: want empty, got %v", got)
	}
}

// TestParseDiff_FileBoundaryFlushesHunk ensures buffered hunk lines from
// file A don't leak into file B when a new `diff --git` header arrives
// (i.e. flushHunk fires at file boundaries, not just hunk boundaries).
func TestParseDiff_FileBoundaryFlushesHunk(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/a.go b/a.go",
		"--- a/a.go",
		"+++ b/a.go",
		"@@ -1,1 +1,1 @@",
		"-a = 1",
		"+a = 2",
		"diff --git a/b.go b/b.go",
		"--- a/b.go",
		"+++ b/b.go",
		"@@ -1,1 +1,1 @@",
		"-b = 1",
		"+b = 2",
		"",
	}, "\n")
	c, err := parseDiff(strings.NewReader(diff), nil)
	if err != nil {
		t.Fatal(err)
	}
	wantAdded := []AddedLine{
		{File: "a.go", LineNum: 1, Content: "a = 2"},
		{File: "b.go", LineNum: 1, Content: "b = 2"},
	}
	if !reflect.DeepEqual(c.Added, wantAdded) {
		t.Errorf("Added: want %v, got %v", wantAdded, c.Added)
	}
	if len(c.Deleted["a.go"]) != 0 || len(c.Deleted["b.go"]) != 0 {
		t.Errorf("Deleted: want empty for both files, got a.go=%v b.go=%v",
			c.Deleted["a.go"], c.Deleted["b.go"])
	}
}

// TestParseDiff_ExcludeListSkipsFile confirms that a diff covering both an
// excluded and a non-excluded file emits entries ONLY for the non-excluded
// one. Mirrors the "Java target/ shouldn't appear in attribution" scenario.
func TestParseDiff_ExcludeListSkipsFile(t *testing.T) {
	excl := loadExcludeListFromContent(t, "target/\n*.class\n")
	diff := strings.Join([]string{
		// File 1: excluded by `target/` rule.
		"diff --git a/target/Build.class b/target/Build.class",
		"new file mode 100644",
		"--- /dev/null",
		"+++ b/target/Build.class",
		"@@ -0,0 +1,3 @@",
		"+CLASSFILE_BYTE_0",
		"+CLASSFILE_BYTE_1",
		"+CLASSFILE_BYTE_2",
		// File 2: not excluded.
		"diff --git a/src/Main.java b/src/Main.java",
		"--- a/src/Main.java",
		"+++ b/src/Main.java",
		"@@ -10,0 +11,2 @@",
		"+    System.out.println(\"hi\");",
		"+    System.out.println(\"bye\");",
		// File 3: excluded by `*.class` glob — proves multiple rules compose.
		"diff --git a/build/Other.class b/build/Other.class",
		"--- a/build/Other.class",
		"+++ b/build/Other.class",
		"@@ -1,1 +1,1 @@",
		"-OLD_BYTECODE",
		"+NEW_BYTECODE",
		"",
	}, "\n")

	c, err := parseDiff(strings.NewReader(diff), excl)
	if err != nil {
		t.Fatal(err)
	}

	// Only src/Main.java should appear.
	wantAdded := []AddedLine{
		{File: "src/Main.java", LineNum: 11, Content: `    System.out.println("hi");`},
		{File: "src/Main.java", LineNum: 12, Content: `    System.out.println("bye");`},
	}
	if !reflect.DeepEqual(c.Added, wantAdded) {
		t.Errorf("Added: want %v, got %v", wantAdded, c.Added)
	}
	for _, excludedPath := range []string{"target/Build.class", "build/Other.class"} {
		if _, ok := c.FileChanges[excludedPath]; ok {
			t.Errorf("FileChanges should not contain excluded path %q", excludedPath)
		}
		if len(c.Deleted[excludedPath]) != 0 {
			t.Errorf("Deleted should not contain %q, got %v", excludedPath, c.Deleted[excludedPath])
		}
	}
}

// loadExcludeListFromContent writes the given content to a tempfile under
// t.TempDir() and loads it via config.LoadExcludeListFrom, so the diff
// regression test exercises the exact loader used at runtime.
func loadExcludeListFromContent(t *testing.T, content string) *config.ExcludeList {
	t.Helper()
	path := filepath.Join(t.TempDir(), "exclude")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write exclude file: %v", err)
	}
	list, err := config.LoadExcludeListFrom(path)
	if err != nil {
		t.Fatalf("LoadExcludeListFrom: %v", err)
	}
	return list
}
