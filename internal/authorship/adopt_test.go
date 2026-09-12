package authorship

import (
	"os"
	"testing"
)

// The reported bug: work on master, then `git checkout -b feature` and commit
// there. HEAD never moved, so the base SHA is unchanged, but the working log is
// keyed by branch — every AI line the session recorded stayed under master/ while
// the commit-time flip read feature/, found nothing, and shipped the note all-Human.
func TestAdoptWorkingLogsAtBase_CarriesAcrossCheckoutB(t *testing.T) {
	repo := t.TempDir()
	const base, rel = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "src/app.go"

	if _, err := Update(repo, "master", base, rel, joinLines("h1", "ai1", "ai2"), joinLines("h1"), ai("claude"), 1); err != nil {
		t.Fatal(err)
	}
	if _, ok := AuthorTypesForFile(repo, "feature", base, rel); ok {
		t.Fatal("precondition: feature branch must start with no log")
	}

	if n := AdoptWorkingLogsAtBase(repo, "feature", base); n != 1 {
		t.Fatalf("adopted %d files, want 1", n)
	}

	types, ok := AuthorTypesForFile(repo, "feature", base, rel)
	if !ok {
		t.Fatal("no working log under feature after adoption")
	}
	for _, ln := range []int{2, 3} {
		if types[ln] != AI {
			t.Errorf("line %d: want AI, got %q", ln, types[ln])
		}
	}
	if types[1] != Human {
		t.Errorf("line 1: want Human, got %q", types[1])
	}

	// The baseline moves with the log — without it the next edit re-seeds from
	// scratch and re-attributes the AI lines to whoever touches the file next.
	if _, ok := loadBaseline(BaselinePath(repo, "feature", base, rel)); !ok {
		t.Error("baseline was not carried over")
	}
	// Moved, not copied: a second switch must not re-adopt the same records.
	if _, err := os.Stat(WorkingLogPath(repo, "master", base, rel)); !os.IsNotExist(err) {
		t.Error("donor log still present after adoption")
	}
}

// A file edited AFTER the branch switch is the fresher record and must win over
// the log left behind on the old branch.
func TestAdoptWorkingLogsAtBase_KeepsPostSwitchLog(t *testing.T) {
	repo := t.TempDir()
	const base, rel = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "src/app.go"

	if _, err := Update(repo, "master", base, rel, joinLines("stale"), "", ai("claude"), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := Update(repo, "feature", base, rel, joinLines("fresh"), "", human(), 2); err != nil {
		t.Fatal(err)
	}

	AdoptWorkingLogsAtBase(repo, "feature", base)

	wl, err := LoadWorkingLog(repo, "feature", base, rel)
	if err != nil || wl == nil {
		t.Fatalf("load: %v", err)
	}
	if got := wl.Lines[0].Author.Type; got != Human {
		t.Errorf("post-switch log was clobbered: line 1 is %q, want Human", got)
	}
	if _, err := os.Stat(WorkingLogPath(repo, "master", base, rel)); !os.IsNotExist(err) {
		t.Error("superseded donor log should be dropped")
	}
}

// Logs at a DIFFERENT base commit describe a different working tree and must be
// left alone — adoption is sound only because the base SHA matches.
func TestAdoptWorkingLogsAtBase_IgnoresOtherBases(t *testing.T) {
	repo := t.TempDir()
	const otherBase = "cccccccccccccccccccccccccccccccccccccccc"
	const thisBase = "dddddddddddddddddddddddddddddddddddddddd"

	if _, err := Update(repo, "master", otherBase, "src/app.go", joinLines("x"), "", ai("claude"), 1); err != nil {
		t.Fatal(err)
	}
	if n := AdoptWorkingLogsAtBase(repo, "feature", thisBase); n != 0 {
		t.Fatalf("adopted %d, want 0", n)
	}
	if _, err := os.Stat(WorkingLogPath(repo, "master", otherBase, "src/app.go")); err != nil {
		t.Error("log at an unrelated base must not be touched")
	}
}

// Deletions live in a shared append-only JSONL, so they are concatenated onto the
// target rather than renamed over it.
func TestAdoptWorkingLogsAtBase_CarriesDeletions(t *testing.T) {
	repo := t.TempDir()
	const base, rel = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", "src/app.go"

	// An AI edit that removes a line records both a working log and a deletion.
	if _, err := Update(repo, "master", base, rel, joinLines("keep"), joinLines("keep", "gone"), ai("claude"), 1); err != nil {
		t.Fatal(err)
	}
	AdoptWorkingLogsAtBase(repo, "feature", base)

	dels, err := LoadDeletions(repo, "feature", base)
	if err != nil {
		t.Fatal(err)
	}
	if a, ok := dels[rel]["gone"]; !ok || a.Type != AI {
		t.Errorf("deletion not carried across the branch switch: %+v", dels)
	}
}
