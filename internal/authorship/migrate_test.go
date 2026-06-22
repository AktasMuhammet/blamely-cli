package authorship

import "testing"

func TestMigrateWorkingLogs(t *testing.T) {
	repo := t.TempDir()
	ai := Author{Type: AI, Tool: "claude", GenType: "chat"}
	// a.txt + b.txt: uncommitted → should migrate. committed.txt: in this commit →
	// its working log is DELETED (note + SQLite hold the attribution now). dup.txt: a
	// fresher log already at baseB → the migrate must not overwrite it.
	for _, f := range []string{"a.txt", "b.txt", "committed.txt", "dup.txt"} {
		if _, err := Update(repo, "main", "baseA", f, "x\n", "", ai, 1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Update(repo, "main", "baseB", "dup.txt", "fresh\n", "", HumanAuthor(), 1); err != nil {
		t.Fatal(err)
	}

	if err := MigrateWorkingLogs(repo, "main", "baseA", "baseB", map[string]bool{"committed.txt": true}); err != nil {
		t.Fatal(err)
	}

	// a.txt + b.txt moved to baseB with updated BaseSHA, preserving AI attribution.
	for _, f := range []string{"a.txt", "b.txt"} {
		wl, _ := LoadWorkingLog(repo, "main", "baseB", f)
		if wl == nil || wl.BaseSHA != "baseB" {
			t.Fatalf("%s not migrated to baseB: %+v", f, wl)
		}
		if len(wl.Lines) == 0 || wl.Lines[0].Author.Type != AI {
			t.Errorf("%s lost its AI attribution after migrate: %+v", f, wl.Lines)
		}
	}
	// committed.txt is deleted from baseA (note + SQLite hold it) and not at baseB.
	if kept, _ := LoadWorkingLog(repo, "main", "baseA", "committed.txt"); kept != nil {
		t.Error("committed.txt working log should be deleted from baseA after commit")
	}
	if moved, _ := LoadWorkingLog(repo, "main", "baseB", "committed.txt"); moved != nil {
		t.Error("committed.txt should NOT have migrated to baseB")
	}
	// dup.txt at baseB kept the fresher (human) log, not the migrated (ai) one.
	if c, _ := LoadWorkingLog(repo, "main", "baseB", "dup.txt"); c == nil || c.Lines[0].Author.Type != Human {
		t.Errorf("fresher baseB/dup.txt should win, got %+v", c)
	}
}
