package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLocateNewString_NotFound(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "f.go", "package x\nconst A = 1\n")
	lr, err := LocateNewString(p, "const MISSING = 0")
	if err != nil {
		t.Fatal(err)
	}
	if lr != nil {
		t.Fatalf("expected nil for not-found string, got %+v", lr)
	}
}

func TestLocateNewString_EmptyNewString(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "f.go", "package x\n")
	lr, err := LocateNewString(p, "")
	if err != nil {
		t.Fatal(err)
	}
	if lr != nil {
		t.Fatalf("expected nil for empty new_string, got %+v", lr)
	}
}

func TestLocateNewString_SingleLine(t *testing.T) {
	dir := t.TempDir()
	// newString is on line 3
	content := "package x\nconst A = 1\nconst B = 99\nconst C = 3\n"
	p := writeFile(t, dir, "f.go", content)
	lr, err := LocateNewString(p, "const B = 99")
	if err != nil {
		t.Fatal(err)
	}
	if lr == nil {
		t.Fatal("expected non-nil")
	}
	if lr.Start != 3 || lr.End != 3 {
		t.Errorf("want start=3 end=3, got start=%d end=%d", lr.Start, lr.End)
	}
	if lr.ContentSHA == "" {
		t.Error("content SHA should not be empty")
	}
}

func TestLocateNewString_MultiLine(t *testing.T) {
	dir := t.TempDir()
	// newString spans lines 2–4
	content := "line1\nlineA\nlineB\nlineC\nline5\n"
	p := writeFile(t, dir, "f.go", content)
	lr, err := LocateNewString(p, "lineA\nlineB\nlineC")
	if err != nil {
		t.Fatal(err)
	}
	if lr == nil {
		t.Fatal("expected non-nil")
	}
	if lr.Start != 2 {
		t.Errorf("want start=2, got %d", lr.Start)
	}
	if lr.End != 4 {
		t.Errorf("want end=4, got %d", lr.End)
	}
}

func TestLocateNewString_FirstLine(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "f.go", "package main\nfunc main() {}\n")
	lr, err := LocateNewString(p, "package main")
	if err != nil {
		t.Fatal(err)
	}
	if lr == nil || lr.Start != 1 || lr.End != 1 {
		t.Errorf("want start=1 end=1, got %+v", lr)
	}
}

func TestLocateNewString_ContentSHAIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	content := "package x\nconst Z = 42\n"
	p := writeFile(t, dir, "f.go", content)
	lr1, _ := LocateNewString(p, "const Z = 42")
	lr2, _ := LocateNewString(p, "const Z = 42")
	if lr1 == nil || lr2 == nil {
		t.Fatal("expected non-nil")
	}
	if lr1.ContentSHA != lr2.ContentSHA {
		t.Errorf("content SHA should be deterministic: %s vs %s", lr1.ContentSHA, lr2.ContentSHA)
	}
}

func TestLineRangeForWholeFile_Empty(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "empty.go", "")
	lr, err := LineRangeForWholeFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if lr != nil {
		t.Fatalf("expected nil for empty file, got %+v", lr)
	}
}

func TestLineRangeForWholeFile_SingleLine(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "s.go", "package x\n")
	lr, err := LineRangeForWholeFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(lr) != 1 {
		t.Fatalf("want 1 range, got %+v", lr)
	}
	if lr[0].Start != 1 || lr[0].End != 1 {
		t.Errorf("want 1..1, got %d..%d", lr[0].Start, lr[0].End)
	}
	if lr[0].ContentSHA != sha256Hex([]byte("package x")) {
		t.Errorf("want per-line content SHA of %q, got %q", "package x", lr[0].ContentSHA)
	}
}

// Each line must carry its OWN content SHA — not one hash for the whole blob —
// so per-line content_sha attribution (which hashes one current line at a time
// to re-locate it after drift) can actually match a "whole file written" event.
// A combined whole-file hash can never equal any single line's hash, which was
// silently leaving e.g. Codex's `patch_apply: "add"` events unattributed.
func TestLineRangeForWholeFile_MultiLine(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "m.go", "a\nb\nc\nd\ne\n")
	lr, err := LineRangeForWholeFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(lr) != 5 {
		t.Fatalf("want 5 ranges, got %+v", lr)
	}
	for i, want := range []string{"a", "b", "c", "d", "e"} {
		r := lr[i]
		if r.Start != i+1 || r.End != i+1 {
			t.Errorf("range %d: want %d..%d, got %d..%d", i, i+1, i+1, r.Start, r.End)
		}
		if r.ContentSHA != sha256Hex([]byte(want)) {
			t.Errorf("range %d: want content SHA of %q, got %q", i, want, r.ContentSHA)
		}
	}
}

func TestAddedOrChangedRanges_SingleLineChange(t *testing.T) {
	old := []byte("a\nb\nc\nd\ne\n")
	// User changes line 3 from "c" to "C-modified"
	new := []byte("a\nb\nC-modified\nd\ne\n")
	got := AddedOrChangedRanges(old, new)
	if len(got) != 1 {
		t.Fatalf("want 1 range, got %d: %+v", len(got), got)
	}
	if got[0].Start != 3 || got[0].End != 3 {
		t.Errorf("want [3,3], got %+v", got[0])
	}
}

func TestAddedOrChangedRanges_InsertedRun(t *testing.T) {
	old := []byte("a\nb\nc\n")
	new := []byte("a\nNEW1\nNEW2\nb\nc\n")
	got := AddedOrChangedRanges(old, new)
	if len(got) != 1 || got[0].Start != 2 || got[0].End != 3 {
		t.Errorf("want [2,3] for inserted run, got %+v", got)
	}
}

func TestAddedOrChangedRanges_MultipleSeparateChanges(t *testing.T) {
	old := []byte("a\nb\nc\nd\ne\nf\n")
	// Change lines 2 and 5
	new := []byte("a\nB-CHANGED\nc\nd\nE-CHANGED\nf\n")
	got := AddedOrChangedRanges(old, new)
	if len(got) != 2 {
		t.Fatalf("want 2 ranges, got %d: %+v", len(got), got)
	}
	if got[0].Start != 2 || got[0].End != 2 {
		t.Errorf("first range: want [2,2], got %+v", got[0])
	}
	if got[1].Start != 5 || got[1].End != 5 {
		t.Errorf("second range: want [5,5], got %+v", got[1])
	}
}

func TestAddedOrChangedRanges_NoChange(t *testing.T) {
	old := []byte("a\nb\nc\n")
	new := []byte("a\nb\nc\n")
	got := AddedOrChangedRanges(old, new)
	if len(got) != 0 {
		t.Errorf("want no ranges, got %+v", got)
	}
}

func TestAddedOrChangedRanges_EmptyOld(t *testing.T) {
	// New file: every line is new.
	got := AddedOrChangedRanges(nil, []byte("a\nb\nc\n"))
	if len(got) != 1 || got[0].Start != 1 || got[0].End != 3 {
		t.Errorf("want [1,3] for all-new file, got %+v", got)
	}
}

func TestAddedOrChangedRanges_AppendAtEnd(t *testing.T) {
	old := []byte("a\nb\n")
	new := []byte("a\nb\nNEW1\nNEW2\n")
	got := AddedOrChangedRanges(old, new)
	if len(got) != 1 || got[0].Start != 3 || got[0].End != 4 {
		t.Errorf("want [3,4] for appended lines, got %+v", got)
	}
}

// TestAddedOrChangedRanges_NewLine verifies that a genuinely new line
// (content not present anywhere in the old file) is flagged.
func TestAddedOrChangedRanges_NewLine(t *testing.T) {
	old := []byte("line1\nline2\n")
	new_ := []byte("line1\nline_new\nline2\n")
	ranges := AddedOrChangedRanges(old, new_)
	if len(ranges) != 1 || ranges[0].Start != 2 || ranges[0].End != 2 {
		t.Errorf("expected [2,2], got %v", ranges)
	}
}

// TestAddedOrChangedRanges_ModifiedLine verifies that a line whose content
// changed (old content gone, new content added) is flagged.
func TestAddedOrChangedRanges_ModifiedLine(t *testing.T) {
	old := []byte("func foo() {\n  return 1\n}\n")
	new_ := []byte("func foo() {\n  return 2\n}\n")
	ranges := AddedOrChangedRanges(old, new_)
	if len(ranges) != 1 || ranges[0].Start != 2 || ranges[0].End != 2 {
		t.Errorf("expected [2,2] for changed return value, got %v", ranges)
	}
}

// TestAddedOrChangedRanges_UnchangedFileYieldsNoRanges confirms that
// identical old/new content produces an empty result.
func TestAddedOrChangedRanges_UnchangedFileYieldsNoRanges(t *testing.T) {
	content := []byte("a\nb\nc\n")
	if got := AddedOrChangedRanges(content, content); len(got) != 0 {
		t.Errorf("identical content: expected no ranges, got %v", got)
	}
}

// TestAddedOrChangedRanges_DuplicateLineIsDetected is a regression test for
// the multiset fix: a file with N identical lines, after adding an (N+1)th
// identical line, must flag the extra as a new line.
func TestAddedOrChangedRanges_DuplicateLineIsDetected(t *testing.T) {
	old := []byte("func foo() {\n\treturn 1\n}\nfunc bar() {\n\treturn 2\n}\n")
	new_ := []byte("func foo() {\n\treturn 1\n}\nfunc bar() {\n\treturn 2\n}\nfunc baz() {\n\treturn 3\n}\n")
	ranges := AddedOrChangedRanges(old, new_)
	if len(ranges) != 1 {
		t.Fatalf("expected 1 range for 3 new lines, got %d: %v", len(ranges), ranges)
	}
	if ranges[0].Start != 7 || ranges[0].End != 9 {
		t.Errorf("expected [7,9] for appended func, got %v", ranges[0])
	}
}

// TestAddedOrChangedRanges_InsertedDuplicateLine verifies inserting a line
// whose content exists elsewhere in the old file IS flagged once the count
// exceeds the old occurrence count.
func TestAddedOrChangedRanges_InsertedDuplicateLine(t *testing.T) {
	old := []byte("func a() {\n\treturn nil\n}\n")
	new_ := []byte("func a() {\n\treturn nil\n}\nfunc b() {\n\treturn nil\n}\n")
	ranges := AddedOrChangedRanges(old, new_)
	if len(ranges) != 1 {
		t.Fatalf("expected 1 range, got %d: %v", len(ranges), ranges)
	}
	if ranges[0].Start != 4 || ranges[0].End != 6 {
		t.Errorf("expected [4,6], got %v", ranges[0])
	}
}

func TestNarrowToChangedLines_OnlyTruncatesContextLines(t *testing.T) {
	// new_string spans absolute file lines 10..14 (5 lines). The first and
	// last lines are unchanged context (also in old_string). Only the
	// middle three lines should remain after narrowing — emitted ONE range
	// per line (11, 12, 13), each carrying its content_sha.
	old := "header\nfooter"
	newStr := "header\nNEW1\nNEW2\nNEW3\nfooter"
	full := LineRange{Start: 10, End: 14}
	ranges, suggested := narrowToChangedLines(old, newStr, full)
	if len(ranges) != 3 {
		t.Fatalf("expected 3 per-line ranges, got %d: %+v", len(ranges), ranges)
	}
	for i, w := range []struct {
		ln   int
		text string
	}{{11, "NEW1"}, {12, "NEW2"}, {13, "NEW3"}} {
		r := ranges[i]
		if r.Start != w.ln || r.End != w.ln {
			t.Errorf("range %d: want single line %d, got [%d,%d]", i, w.ln, r.Start, r.End)
		}
		if want := sha256Hex([]byte(w.text)); r.ContentSHA != want {
			t.Errorf("range %d (L%d): content_sha mismatch", i, w.ln)
		}
	}
	if suggested != 3 {
		t.Errorf("expected suggested=3, got %d", suggested)
	}
}

func TestNarrowToChangedLines_IdenticalCreditsFullPerLine(t *testing.T) {
	// When old == new (no real change), there are no new lines; we fall back
	// to crediting the located span so the AI still appears in the edit log —
	// but still LINE BY LINE (each line with its sha), never a coarse range
	// that could be matched positionally.
	full := LineRange{Start: 5, End: 7}
	ranges, suggested := narrowToChangedLines("a\nb\nc", "a\nb\nc", full)
	if len(ranges) != 3 {
		t.Fatalf("expected 3 per-line ranges, got %d: %+v", len(ranges), ranges)
	}
	for i, w := range []struct {
		ln   int
		text string
	}{{5, "a"}, {6, "b"}, {7, "c"}} {
		r := ranges[i]
		if r.Start != w.ln || r.End != w.ln || r.ContentSHA != sha256Hex([]byte(w.text)) {
			t.Errorf("range %d: want L%d sha(%q), got %+v", i, w.ln, w.text, r)
		}
	}
	if suggested != 3 {
		t.Errorf("expected suggested=3 (full span size), got %d", suggested)
	}
}

func TestCountAddedLines(t *testing.T) {
	if got := CountAddedLines("a", "a\nb\nc"); got != 2 {
		t.Errorf("CountAddedLines: want 2, got %d", got)
	}
	if got := CountAddedLines("", "a\nb\nc"); got != 3 {
		t.Errorf("CountAddedLines (empty old): want 3, got %d", got)
	}
	if got := CountAddedLines("a\nb", "a\nb"); got != 0 {
		t.Errorf("CountAddedLines (no change): want 0, got %d", got)
	}
}

// TestNarrowToChangedLines_PerLineSha asserts the Edit/MultiEdit recording path
// emits ONE range per changed line, each non-blank line carrying its content_sha
// (sans trailing \r). A coarse multi-line range with no sha would be matched
// positionally at commit time and would mislabel a human line inserted inside
// the AI block as AI — the bug this guards against.
func TestNarrowToChangedLines_PerLineSha(t *testing.T) {
	// new_string = two context lines (shared with old) + 3 genuinely-new lines.
	oldStr := "ctxA\nctxB"
	newStr := "ctxA\nctxB\nNEW1\nNEW2\nNEW3"
	// Pretend LocateNewString placed new_string at file lines 224..228.
	full := LineRange{Start: 224, End: 228}

	ranges, suggested := narrowToChangedLines(oldStr, newStr, full)

	if suggested != 3 {
		t.Fatalf("suggested: want 3 new lines, got %d", suggested)
	}
	// Expect exactly the 3 new lines, each a single-line range with a sha.
	if len(ranges) != 3 {
		t.Fatalf("want 3 per-line ranges, got %d: %+v", len(ranges), ranges)
	}
	wantLines := []struct {
		ln   int
		text string
	}{{226, "NEW1"}, {227, "NEW2"}, {228, "NEW3"}}
	for i, w := range wantLines {
		r := ranges[i]
		if r.Start != w.ln || r.End != w.ln {
			t.Errorf("range %d: want single line %d, got [%d,%d]", i, w.ln, r.Start, r.End)
		}
		if want := sha256Hex([]byte(w.text)); r.ContentSHA != want {
			t.Errorf("range %d (L%d): content_sha mismatch\n want %s\n  got %s", i, w.ln, want, r.ContentSHA)
		}
	}
}

// TestNarrowToChangedLines_BlankLineNoSha: blank lines inside the changed block
// still get a per-line range (so they're counted) but carry no content_sha,
// matching perLineShaRangesFromContent's convention.
func TestNarrowToChangedLines_BlankLineNoSha(t *testing.T) {
	oldStr := "keep"
	newStr := "keep\nA\n\nB"
	full := LineRange{Start: 10, End: 13}
	ranges, suggested := narrowToChangedLines(oldStr, newStr, full)
	if suggested != 3 {
		t.Fatalf("suggested: want 3, got %d", suggested)
	}
	if len(ranges) != 3 {
		t.Fatalf("want 3 ranges, got %d: %+v", len(ranges), ranges)
	}
	// Lines 11 (A), 12 (blank), 13 (B).
	if ranges[1].Start != 12 || ranges[1].ContentSHA != "" {
		t.Errorf("blank line should be L12 with empty sha, got %+v", ranges[1])
	}
	if ranges[0].ContentSHA == "" || ranges[2].ContentSHA == "" {
		t.Errorf("non-blank lines must carry a sha: %+v", ranges)
	}
	if ranges[1].ContentSHANorm != "" {
		t.Errorf("blank line should have empty content_sha_norm, got %+v", ranges[1])
	}
	if ranges[0].ContentSHANorm == "" || ranges[2].ContentSHANorm == "" {
		t.Errorf("non-blank lines must carry a content_sha_norm: %+v", ranges)
	}
}

// ---- NormalizeLineText / content_sha_norm ----

func TestNormalizeLineText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  foo(a, b)  ", "foo(a, b)"},
		{"\tfoo(a,  b)", "foo(a, b)"},
		{"a    b\tc", "a b c"},
		{"", ""},
		{"   ", ""},
		{"already normal", "already normal"},
	}
	for _, c := range cases {
		if got := NormalizeLineText(c.in); got != c.want {
			t.Errorf("NormalizeLineText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestLineRangeForWholeFile_NormalizedHashSurvivesReindent reproduces the
// autoformatter-drift scenario: the same logical line, indented differently,
// must produce the same content_sha_norm even though content_sha differs.
func TestLineRangeForWholeFile_NormalizedHashSurvivesReindent(t *testing.T) {
	dir := t.TempDir()
	tabFile := writeFile(t, dir, "tabs.go", "\treturn 1\n")
	spaceFile := writeFile(t, dir, "spaces.go", "    return 1\n")

	tabLR, err := LineRangeForWholeFile(tabFile)
	if err != nil {
		t.Fatal(err)
	}
	spaceLR, err := LineRangeForWholeFile(spaceFile)
	if err != nil {
		t.Fatal(err)
	}

	if tabLR[0].ContentSHA == spaceLR[0].ContentSHA {
		t.Fatalf("content_sha should differ across reindent (tab vs spaces)")
	}
	if tabLR[0].ContentSHANorm == "" || tabLR[0].ContentSHANorm != spaceLR[0].ContentSHANorm {
		t.Errorf("content_sha_norm should match across reindent: tab=%q space=%q",
			tabLR[0].ContentSHANorm, spaceLR[0].ContentSHANorm)
	}
}

// TestLineRangeForWholeFile_BlankLineNoNormSha mirrors the blank-line
// content_sha convention for content_sha_norm.
func TestLineRangeForWholeFile_BlankLineNoNormSha(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "blank.go", "a\n   \nb\n")
	lr, err := LineRangeForWholeFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(lr) != 3 {
		t.Fatalf("want 3 ranges, got %+v", lr)
	}
	if lr[1].ContentSHANorm != "" {
		t.Errorf("whitespace-only line should have empty content_sha_norm, got %q", lr[1].ContentSHANorm)
	}
	if lr[0].ContentSHANorm == "" || lr[2].ContentSHANorm == "" {
		t.Errorf("non-blank lines must carry a content_sha_norm: %+v", lr)
	}
}

// TestAddedOrMovedLineRanges_DetectsMove reproduces commit bee1fa6d: the AI
// deletes a block and re-adds a pre-existing line lower down (a move). The
// multiset diff drops it (content count unchanged) → Human; the LCS diff must
// report it at its new position so it attributes to the AI.
func TestAddedOrMovedLineRanges_DetectsMove(t *testing.T) {
	old := []byte("<body>\n  <h1>helloo</h1>\n  <p>a</p>\n  <p>b</p>\n  <footer>x</footer>\n</body>\n")
	// h1 removed from the top, the <p>s deleted, h1 re-added before </body>.
	new_ := []byte("<body>\n  \n  <footer>x</footer>\n  <h1>helloo</h1>\n</body>\n")

	ranges := addedOrMovedLineRanges(old, new_)
	// The moved <h1> is at line 4 of new_; it must be reported.
	movedReported := false
	for _, r := range ranges {
		if 4 >= r.Start && 4 <= r.End {
			movedReported = true
		}
	}
	if !movedReported {
		t.Fatalf("moved <h1> (new line 4) not reported as added/moved; ranges=%+v", ranges)
	}
	// Multiset diff would have missed it — assert the regression direction.
	multiset := AddedOrChangedRanges(old, new_)
	msReported := false
	for _, r := range multiset {
		if 4 >= r.Start && 4 <= r.End {
			msReported = true
		}
	}
	if msReported {
		t.Log("note: multiset happened to report it too (still fine)")
	}
}

// TestAddedOrMovedLineRanges_NoOverCreditOnShift ensures lines that only SHIFT
// position (same content, same order, due to an insertion above) are NOT
// reported — only the genuinely-inserted line is.
func TestAddedOrMovedLineRanges_NoOverCreditOnShift(t *testing.T) {
	old := []byte("a\nb\nc\n")
	new_ := []byte("INSERTED\na\nb\nc\n") // everything shifts down by one
	ranges := addedOrMovedLineRanges(old, new_)
	// Only line 1 (INSERTED) should be reported; a/b/c stay in the LCS.
	if len(ranges) != 1 || ranges[0].Start != 1 || ranges[0].End != 1 {
		t.Fatalf("shift over-credited: want only [1-1], got %+v", ranges)
	}
}

// TestAddedOrMovedLineRanges_UnchangedYieldsNothing — identical content → no ranges.
func TestAddedOrMovedLineRanges_UnchangedYieldsNothing(t *testing.T) {
	c := []byte("x\ny\nz\n")
	if got := addedOrMovedLineRanges(c, c); len(got) != 0 {
		t.Fatalf("unchanged content yielded %+v", got)
	}
}

// TestAddedOrMovedLineRanges_PureInsertAndDuplicate keeps parity with the
// multiset diff for the non-move cases it already handled.
func TestAddedOrMovedLineRanges_PureInsertAndDuplicate(t *testing.T) {
	// New genuine line in the middle.
	old := []byte("a\nb\nc\n")
	new_ := []byte("a\nNEW\nb\nc\n")
	got := addedOrMovedLineRanges(old, new_)
	if len(got) != 1 || got[0].Start != 2 || got[0].End != 2 {
		t.Fatalf("insert: want [2-2], got %+v", got)
	}
	// Duplicated line beyond old count.
	got = addedOrMovedLineRanges([]byte("}\n"), []byte("}\n}\n"))
	if len(got) != 1 || got[0].Start != 2 {
		t.Fatalf("duplicate: want [2-..], got %+v", got)
	}
}
