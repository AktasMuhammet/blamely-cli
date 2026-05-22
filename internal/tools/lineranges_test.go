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
	if lr == nil {
		t.Fatal("expected non-nil")
	}
	if lr.Start != 1 || lr.End != 1 {
		t.Errorf("want 1..1, got %d..%d", lr.Start, lr.End)
	}
}

func TestLineRangeForWholeFile_MultiLine(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "m.go", "a\nb\nc\nd\ne\n")
	lr, err := LineRangeForWholeFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if lr == nil || lr.End != 5 {
		t.Errorf("want end=5, got %+v", lr)
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
