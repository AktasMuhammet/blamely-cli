package gitnotes

import (
	"testing"
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
