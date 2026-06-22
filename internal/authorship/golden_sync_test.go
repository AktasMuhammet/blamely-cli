package authorship

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The golden vectors are the cross-language contract, but the plugins live in
// sibling repos and keep COPIES (vscode-plugin/src/test, intellij-plugin/src/test/
// resources). This guard fails if a copy drifts from the canonical file — but only
// in the dev monorepo layout where the siblings are present; it SKIPS when they're
// absent (a CI checkout of blamely-cli alone), so it never produces false failures.
func TestGoldenVectorsCopiesInSync(t *testing.T) {
	canonical, err := os.ReadFile(filepath.Join("testdata", "golden_vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Relative to this package dir: blamely-cli/internal/authorship → up 3 →
	// the workspace root that holds the sibling plugin repos.
	copies := []string{
		filepath.Join("..", "..", "..", "vscode-plugin", "src", "test", "golden_vectors.json"),
		filepath.Join("..", "..", "..", "intellij-plugin", "src", "test", "resources", "golden_vectors.json"),
	}
	checked := 0
	for _, c := range copies {
		data, err := os.ReadFile(c)
		if os.IsNotExist(err) {
			continue // sibling repo not present (CI) — nothing to compare
		}
		if err != nil {
			t.Errorf("read %s: %v", c, err)
			continue
		}
		checked++
		if !bytes.Equal(data, canonical) {
			t.Errorf("%s has drifted from the canonical golden_vectors.json — re-sync it", c)
		}
	}
	t.Logf("checked %d plugin copy/copies against canonical", checked)
}
