package tools

import (
	"os"
	"path/filepath"
	"testing"
)

// Regression: a Copilot apply_patch `*** Delete File:` directive carries no -lines, so
// the removed content isn't spelled out in the patch. The parser must read the file's
// content and record every line as removed; otherwise the AI deletion has no edit and
// the commit credits the removal to Human (see commit ba5fc9b0 in the field repro).
func TestApplyPatch_DeleteFile_RecordsRemovedLines(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "scenerio_5.txt")
	if err := os.WriteFile(f, []byte("kerim\nkerim\nkerim\nkerim\nkerim\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	body := "*** Begin Patch\n*** Delete File: " + f + "\n*** End Patch\n"
	files := parseApplyPatchPerLine(body)

	if len(files) == 0 {
		t.Fatal("delete-file produced no patch-file entry — the deletion would fall to Human")
	}
	removed := 0
	for _, fe := range files {
		removed += len(fe.removed)
		if len(fe.added) != 0 {
			t.Errorf("delete-file must record no added lines, got %d", len(fe.added))
		}
	}
	if removed != 5 {
		t.Fatalf("want 5 removed-line hashes for the deleted 5-line file, got %d", removed)
	}
}
