package tools

import "testing"

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]bool{}
	for _, x := range a {
		m[x] = true
	}
	for _, x := range b {
		if !m[x] {
			return false
		}
	}
	return true
}

func TestBashRedirectAndRmTargets(t *testing.T) {
	// Real transcript commands have actual newlines (JSON-decoded \n).
	w := bashRedirectTargets("cat <<'EOF' > sukru4.html\n<html>\nEOF\ngit add sukru4.html && git commit -m \"x\"")
	if !eq(w, []string{"sukru4.html"}) {
		t.Errorf("redirect targets = %v", w)
	}
	if got := bashRedirectTargets("ls 2>/dev/null > out.txt"); !eq(got, []string{"out.txt"}) {
		t.Errorf("redirect (dev filtered) = %v", got)
	}
	d := bashRmTargets("rm muhamemt.html && git add -A && git commit -m \"Remove\"")
	if !eq(d, []string{"muhamemt.html"}) {
		t.Errorf("rm targets = %v", d)
	}
	d2 := bashRmTargets("rm -f hello*.html welcome.html && git commit")
	if !eq(d2, []string{"hello*.html", "welcome.html"}) {
		t.Errorf("rm targets2 = %v", d2)
	}
}

func TestMatchesFileOp(t *testing.T) {
	if !MatchesFileOp("sukru4.html", []string{"sukru4.html"}) {
		t.Error("exact")
	}
	if !MatchesFileOp("hello.html", []string{"hello*.html"}) {
		t.Error("glob")
	}
	if !MatchesFileOp("src/x.html", []string{"x.html"}) {
		t.Error("basename")
	}
	if MatchesFileOp("other.html", []string{"sukru4.html"}) {
		t.Error("false positive")
	}
}
