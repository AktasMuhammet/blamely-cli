package authorship

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// goldenVectors is the shared cross-language contract (testdata/golden_vectors.json).
// The VS Code (TS) and IntelliJ (Kotlin) ports run the SAME file; if any of the
// three drifts, its run of these cases fails. See docs/attribution-v2-design.md §6.
type goldenFile struct {
	Version string       `json:"version"`
	Cases   []goldenCase `json:"cases"`
}

type goldenCase struct {
	Name  string `json:"name"`
	Prior []struct {
		Start  int        `json:"start"`
		End    int        `json:"end"`
		Author AuthorType `json:"author"`
	} `json:"prior"`
	Baseline string `json:"baseline"`
	New      string `json:"new"`
	Author   struct {
		Author  AuthorType `json:"author"`
		Tool    string     `json:"tool"`
		GenType string     `json:"gen_type"`
	} `json:"author"`
	Expect []AuthorType `json:"expect"`
}

func TestGoldenVectors(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "golden_vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var gf goldenFile
	if err := json.Unmarshal(data, &gf); err != nil {
		t.Fatalf("parse golden vectors: %v", err)
	}
	if len(gf.Cases) == 0 {
		t.Fatal("no golden cases")
	}

	for _, c := range gf.Cases {
		t.Run(c.Name, func(t *testing.T) {
			var prior *WorkingLog
			if c.Prior != nil {
				prior = &WorkingLog{Schema: WorkingLogSchema}
				for _, r := range c.Prior {
					prior.Lines = append(prior.Lines, LineAttribution{
						Start: r.Start, End: r.End, Author: Author{Type: r.Author},
					})
				}
			}
			author := Author{Type: c.Author.Author, Tool: c.Author.Tool, GenType: c.Author.GenType}

			wl := Attribute(prior, c.Baseline, c.New, author, 1)
			got := typesByLine(wl, len(c.Expect))

			if len(got) != len(c.Expect) {
				t.Fatalf("line count: got %d, want %d", len(got), len(c.Expect))
			}
			for i := range c.Expect {
				if got[i] != c.Expect[i] {
					t.Errorf("line %d: got %q, want %q", i+1, got[i], c.Expect[i])
				}
			}
		})
	}
}
