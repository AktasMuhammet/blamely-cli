package authorship

import (
	"os"
	"testing"
)

// The pre-edit baseline (record --pre) is the only record of what a file looked
// like before an agent ran. The commit seed must not overwrite it: doing so makes
// the next edit diff against the COMMIT, so every uncommitted line the user had
// typed gets swept into the agent's claim.
func TestSeedWorkingLog_KeepsCapturedBaseline(t *testing.T) {
	repo := t.TempDir()
	const (
		branch = "main"
		base   = "abc123"
		rel    = "app.py"
		// What the commit holds, and what its note attributes.
		committed = "c1\nc2\n"
		// What the pre-hook captured: the commit plus a line nobody observed.
		captured = "c1\nc2\nhuman_typed_this\n"
	)
	if err := PutBaseline(repo, branch, base, rel, captured); err != nil {
		t.Fatal(err)
	}

	committedLines := []LineAttribution{
		{Start: 1, End: 2, Author: Author{Type: AI, Tool: "claude", GenType: "cli"}},
	}
	if err := SeedWorkingLog(repo, branch, base, rel, committed, committedLines, 0); err != nil {
		t.Fatalf("SeedWorkingLog: %v", err)
	}

	wl, err := LoadWorkingLog(repo, branch, base, rel)
	if err != nil || wl == nil {
		t.Fatalf("LoadWorkingLog: %v (wl=%v)", err, wl)
	}
	byLine := map[int]Author{}
	for _, r := range wl.Lines {
		for ln := r.Start; ln <= r.End; ln++ {
			byLine[ln] = r.Author
		}
	}
	// The committed lines keep the authorship the note recorded...
	for _, ln := range []int{1, 2} {
		if a := byLine[ln]; a.Type != AI || a.Tool != "claude" {
			t.Errorf("line %d = %+v, want the committed AI/claude attribution", ln, a)
		}
	}
	// ...and the uncommitted line nobody observed is Human, not the agent's.
	if a := byLine[3]; a.Type != Human {
		t.Errorf("line 3 = %+v, want Human (unobserved uncommitted work)", a)
	}
	// The captured baseline itself must survive, or the next edit diffs against the
	// commit again and we are back to the original bug.
	data, err := os.ReadFile(BaselinePath(repo, branch, base, rel))
	if err != nil {
		t.Fatalf("baseline gone: %v", err)
	}
	if string(data) != captured {
		t.Errorf("baseline = %q, want the captured %q", data, captured)
	}
}

// With no captured baseline the seed behaves exactly as before: it writes the
// committed attributions and the committed content as the baseline.
func TestSeedWorkingLog_NoCapturedBaseline(t *testing.T) {
	repo := t.TempDir()
	const (
		branch    = "main"
		base      = "abc123"
		rel       = "app.py"
		committed = "c1\nc2\n"
	)
	lines := []LineAttribution{{Start: 1, End: 2, Author: Author{Type: AI, Tool: "codex", GenType: "cli"}}}
	if err := SeedWorkingLog(repo, branch, base, rel, committed, lines, 0); err != nil {
		t.Fatalf("SeedWorkingLog: %v", err)
	}
	wl, err := LoadWorkingLog(repo, branch, base, rel)
	if err != nil || wl == nil {
		t.Fatalf("LoadWorkingLog: %v", err)
	}
	if len(wl.Lines) != 1 || wl.Lines[0].Start != 1 || wl.Lines[0].End != 2 || wl.Lines[0].Author.Tool != "codex" {
		t.Errorf("lines = %+v, want the committed 1-2 AI/codex span verbatim", wl.Lines)
	}
	if wl.UpdatedMS != 0 {
		t.Errorf("UpdatedMS = %d, want 0 left unset for the next Update to stamp", wl.UpdatedMS)
	}
	data, err := os.ReadFile(BaselinePath(repo, branch, base, rel))
	if err != nil || string(data) != committed {
		t.Errorf("baseline = %q / %v, want the committed content", data, err)
	}
}

// An identical captured baseline is not a divergence: the seed must still leave
// the committed attributions untouched (and not restamp them).
func TestSeedWorkingLog_CapturedBaselineMatchesCommit(t *testing.T) {
	repo := t.TempDir()
	const (
		branch    = "main"
		base      = "abc123"
		rel       = "app.py"
		committed = "c1\nc2\n"
	)
	if err := PutBaseline(repo, branch, base, rel, committed); err != nil {
		t.Fatal(err)
	}
	lines := []LineAttribution{{Start: 1, End: 2, Author: Author{Type: Human, GenType: "human"}}}
	if err := SeedWorkingLog(repo, branch, base, rel, committed, lines, 0); err != nil {
		t.Fatalf("SeedWorkingLog: %v", err)
	}
	wl, err := LoadWorkingLog(repo, branch, base, rel)
	if err != nil || wl == nil {
		t.Fatalf("LoadWorkingLog: %v", err)
	}
	if len(wl.Lines) != 1 || wl.Lines[0].End != 2 || wl.Lines[0].Author.Type != Human {
		t.Errorf("lines = %+v, want the committed 1-2 Human span verbatim", wl.Lines)
	}
}
