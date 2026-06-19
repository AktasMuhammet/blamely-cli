package daemon

import "testing"

func TestCompactNum(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{418, "418"},
		{999, "999"},
		{1000, "1.0k"},
		{1372, "1.4k"},
		{73332, "73.3k"},
		{999999, "1000.0k"},
		{1_000_000, "1.0M"},
		{2_500_000, "2.5M"},
	}
	for _, c := range cases {
		if got := compactNum(c.in); got != c.want {
			t.Errorf("compactNum(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestShortID(t *testing.T) {
	if got := shortID("11111111-2222-3333-4444-555555555555"); got != "11111111" {
		t.Errorf("shortID(uuid) = %q, want 11111111", got)
	}
	if got := shortID("abc"); got != "abc" {
		t.Errorf("shortID(short) = %q, want abc (unchanged)", got)
	}
	if got := shortID(""); got != "" {
		t.Errorf("shortID(empty) = %q, want empty", got)
	}
}
