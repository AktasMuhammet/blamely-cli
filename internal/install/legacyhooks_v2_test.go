package install

import (
	"os"
	"path/filepath"
	"testing"
)

// RemoveLegacyRepoHooks must purge legacy .git/blamely artifacts but PRESERVE the
// Attribution v2 working logs — it runs at the start of `blamely attribute`, which
// reads the working log a moment later. A regression here silently breaks the flip.
func TestRemoveLegacyRepoHooks_PreservesWorkingLogs(t *testing.T) {
	repo := initRepo(t)
	gitDir := filepath.Join(repo, ".git")
	legacy := filepath.Join(gitDir, "blamely", "hookRunner-pre-push.sh")
	wl := filepath.Join(gitDir, "blamely", "working_logs", "main", "abc123", "app.py.json")
	for _, p := range []string{legacy, wl} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	RemoveLegacyRepoHooks(repo)

	if _, err := os.Stat(wl); err != nil {
		t.Errorf("working log must survive legacy cleanup: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy runner must be removed, got err=%v", err)
	}
}
