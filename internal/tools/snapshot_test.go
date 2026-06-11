package tools

import "testing"

func TestRemovedLinesForTextEditRange_MultiLineReplaceDropsOldLines(t *testing.T) {
	snapshot := "line1\nline2\nline3\nline4\nline5\n"
	// VS Code half-open range: [2,4) covers lines 2 and 3.
	got := removedLinesForTextEditRange(snapshot, 2, 4, "replacement")
	if len(got) != 2 {
		t.Fatalf("want 2 removed lines, got %d: %+v", len(got), got)
	}
	want := []string{sha256Hex([]byte("line2")), sha256Hex([]byte("line3"))}
	for i, w := range want {
		if got[i].ContentSHA != w {
			t.Errorf("removed[%d].ContentSHA = %q, want %q", i, got[i].ContentSHA, w)
		}
	}
}

func TestRemovedLinesForTextEditRange_SkipsBlankLines(t *testing.T) {
	snapshot := "line1\nline2\n\nline4\nline5\n"
	got := removedLinesForTextEditRange(snapshot, 2, 5, "X")
	if len(got) != 2 {
		t.Fatalf("want 2 removed lines (blank skipped), got %d: %+v", len(got), got)
	}
	want := []string{sha256Hex([]byte("line2")), sha256Hex([]byte("line4"))}
	for i, w := range want {
		if got[i].ContentSHA != w {
			t.Errorf("removed[%d].ContentSHA = %q, want %q", i, got[i].ContentSHA, w)
		}
	}
}

func TestRemovedLinesForTextEditRange_EndLineClampedToFileLength(t *testing.T) {
	snapshot := "line1\nline2\nline3\n"
	// endLine extends past EOF — clamp to the last line.
	got := removedLinesForTextEditRange(snapshot, 2, 100, "")
	if len(got) != 2 {
		t.Fatalf("want 2 removed lines, got %d: %+v", len(got), got)
	}
}

func TestRemovedLinesForTextEditRange_NoRemovalCases(t *testing.T) {
	snapshot := "line1\nline2\nline3\n"
	cases := map[string]struct {
		snapshot           string
		startLine, endLine int
		newText            string
	}{
		"empty snapshot":       {"", 1, 2, "x"},
		"endLine == startLine": {snapshot, 2, 2, "x"}, // pure insertion, no removal signal
		"startLine before 1":   {snapshot, 0, 2, "x"},
		"startLine past EOF":   {snapshot, 10, 12, "x"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := removedLinesForTextEditRange(c.snapshot, c.startLine, c.endLine, c.newText); got != nil {
				t.Errorf("want nil, got %+v", got)
			}
		})
	}
}
