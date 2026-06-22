package authorship

import (
	"sort"
	"testing"
)

// ListWorkingLogs returns every tracked file's log under (repo,branch,base) and
// skips the .baselines content subtree.
func TestListWorkingLogs(t *testing.T) {
	repo := t.TempDir()
	if _, err := Update(repo, "main", "base1", "a.txt", "x\n", "", HumanAuthor(), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := Update(repo, "main", "base1", "sub/b.txt", "y\n", "",
		Author{Type: AI, Tool: "claude", GenType: "chat"}, 1); err != nil {
		t.Fatal(err)
	}
	// A different base must NOT be included.
	if _, err := Update(repo, "main", "base2", "c.txt", "z\n", "", HumanAuthor(), 1); err != nil {
		t.Fatal(err)
	}

	logs, err := ListWorkingLogs(repo, "main", "base1")
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, wl := range logs {
		files = append(files, wl.File)
	}
	sort.Strings(files)
	if len(files) != 2 || files[0] != "a.txt" || files[1] != "sub/b.txt" {
		t.Fatalf("want [a.txt sub/b.txt], got %v", files)
	}

	// Empty/missing dir is not an error.
	empty, err := ListWorkingLogs(repo, "main", "nope")
	if err != nil || len(empty) != 0 {
		t.Errorf("missing base: want empty/no error, got %d logs err=%v", len(empty), err)
	}
}
