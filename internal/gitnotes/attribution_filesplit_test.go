package gitnotes

import (
	"encoding/json"
	"strings"
	"testing"
)

// Each file entry must carry the AI/Human split of its added and deleted lines
// under the same JSON keys the totals use.
func TestRecomputeFileLineSplits(t *testing.T) {
	note := &Note{
		Files: []FileEntry{
			{
				Path:    "mixed.go",
				Added:   5,
				Deleted: 3,
				Lines: []RangeEntry{
					{Start: 1, End: 3, Type: "add", AuthorType: "AI", Tool: "claude"},
					{Start: 4, End: 5, Type: "add", AuthorType: "Human"},
					{Start: 10, End: 10, Type: "delete", AuthorType: "AI", Tool: "claude"},
					{Start: 11, End: 12, Type: "delete", AuthorType: "Human"},
				},
			},
			{
				Path:  "human-only.go",
				Added: 2,
				Lines: []RangeEntry{
					{Start: 1, End: 2, Type: "add", AuthorType: "Human"},
				},
			},
			// file_lines stripped by config: no ranges, so the AI share is
			// unknowable — everything must fall to Human, not be lost.
			{Path: "stripped.go", Added: 4, Deleted: 2},
		},
	}

	recomputeFileLineSplits(note)

	mixed := note.Files[0]
	if mixed.AIAdded != 3 || mixed.HumanAdded != 2 {
		t.Fatalf("mixed added split = ai %d / human %d, want 3 / 2", mixed.AIAdded, mixed.HumanAdded)
	}
	if mixed.AIDeleted != 1 || mixed.HumanDeleted != 2 {
		t.Fatalf("mixed deleted split = ai %d / human %d, want 1 / 2", mixed.AIDeleted, mixed.HumanDeleted)
	}

	humanOnly := note.Files[1]
	if humanOnly.AIAdded != 0 || humanOnly.HumanAdded != 2 || humanOnly.AIDeleted != 0 || humanOnly.HumanDeleted != 0 {
		t.Fatalf("human-only split = %+v", humanOnly)
	}

	stripped := note.Files[2]
	if stripped.AIAdded != 0 || stripped.HumanAdded != 4 || stripped.AIDeleted != 0 || stripped.HumanDeleted != 2 {
		t.Fatalf("stripped split = %+v", stripped)
	}

	// The persisted JSON must expose the split under the totals-style keys.
	body, err := json.Marshal(mixed)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"added_lines":5`, `"deleted_lines":3`, `"ai_added_lines":3`, `"human_added_lines":2`, `"ai_deleted_lines":1`, `"human_deleted_lines":2`} {
		if !strings.Contains(string(body), key) {
			t.Fatalf("marshaled file entry missing %s: %s", key, body)
		}
	}
	if strings.Contains(string(body), `"added":`) || strings.Contains(string(body), `"deleted":`) {
		t.Fatalf("marshaled file entry still emits legacy added/deleted keys: %s", body)
	}
}

// Old notes wrote the bare added / deleted keys; reading one must still fill
// the totals (legacy fallback in FileEntry.UnmarshalJSON).
func TestFileEntryUnmarshalLegacyKeys(t *testing.T) {
	var fe FileEntry
	if err := json.Unmarshal([]byte(`{"path":"old.go","added":7,"deleted":4}`), &fe); err != nil {
		t.Fatal(err)
	}
	if fe.Added != 7 || fe.Deleted != 4 {
		t.Fatalf("legacy note parsed as added %d / deleted %d, want 7 / 4", fe.Added, fe.Deleted)
	}
	// New-style keys win when both are present.
	fe = FileEntry{}
	if err := json.Unmarshal([]byte(`{"path":"new.go","added_lines":2,"deleted_lines":1,"added":9,"deleted":9}`), &fe); err != nil {
		t.Fatal(err)
	}
	if fe.Added != 2 || fe.Deleted != 1 {
		t.Fatalf("mixed-key note parsed as added %d / deleted %d, want 2 / 1", fe.Added, fe.Deleted)
	}
}

// The invariant ai + human == total must hold even if ranges over-cover the
// file's recorded totals (defensive: never emit a negative human share).
func TestRecomputeFileLineSplitsClamps(t *testing.T) {
	note := &Note{
		Files: []FileEntry{
			{
				Path:    "over.go",
				Added:   1,
				Deleted: 0,
				Lines: []RangeEntry{
					{Start: 1, End: 5, Type: "add", AuthorType: "AI", Tool: "claude"},
					{Start: 9, End: 9, Type: "delete", AuthorType: "AI", Tool: "claude"},
				},
			},
		},
	}
	recomputeFileLineSplits(note)
	fe := note.Files[0]
	if fe.AIAdded != 1 || fe.HumanAdded != 0 {
		t.Fatalf("added split = ai %d / human %d, want 1 / 0", fe.AIAdded, fe.HumanAdded)
	}
	if fe.AIDeleted != 0 || fe.HumanDeleted != 0 {
		t.Fatalf("deleted split = ai %d / human %d, want 0 / 0", fe.AIDeleted, fe.HumanDeleted)
	}
}
