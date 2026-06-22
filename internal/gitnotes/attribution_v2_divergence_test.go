package gitnotes

import "testing"

// persistDivergence + ReadDivergenceSummary make the soak measurable: records
// accumulate per commit and aggregate into the §12 gate summary.
func TestDivergenceSummary(t *testing.T) {
	repo := t.TempDir()
	persistDivergence(repo, "aaaaaaaa", v2Divergence{Compared: 3, Divergent: 0, Files: 1})
	persistDivergence(repo, "bbbbbbbb", v2Divergence{Compared: 2, Divergent: 1, V1AI: 0, V2AI: 1, Files: 1})
	persistDivergence(repo, "cccccccc", v2Divergence{Compared: 0, NoLog: 2})

	s, err := ReadDivergenceSummary(repo, 10)
	if err != nil {
		t.Fatal(err)
	}
	if s.Commits != 3 {
		t.Errorf("commits: got %d, want 3", s.Commits)
	}
	if s.ComparedLines != 5 {
		t.Errorf("compared: got %d, want 5", s.ComparedLines)
	}
	if s.DivergentLines != 1 {
		t.Errorf("divergent: got %d, want 1", s.DivergentLines)
	}
	if s.NoLogFiles != 2 {
		t.Errorf("no-log files: got %d, want 2", s.NoLogFiles)
	}
	if len(s.Recent) != 1 || s.Recent[0].Commit != "bbbbbbbb" {
		t.Errorf("recent divergent: want [bbbbbbbb], got %+v", s.Recent)
	}

	// Missing log → zero summary, no error.
	if s2, err := ReadDivergenceSummary(t.TempDir(), 10); err != nil || s2.Commits != 0 {
		t.Errorf("missing log: want empty/no-error, got %+v err=%v", s2, err)
	}
}
