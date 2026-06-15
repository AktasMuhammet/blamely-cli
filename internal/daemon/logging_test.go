package daemon

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func ts(t time.Time) string {
	return t.Format(logTimeLayout)
}

func TestPruneOldLines_DropsOlderThanCutoff(t *testing.T) {
	now := time.Now()
	old := now.Add(-48 * time.Hour)
	recent := now.Add(-1 * time.Hour)
	cutoff := now.Add(-logRetention)

	data := []byte(strings.Join([]string{
		ts(old) + " old line one",
		ts(old) + " old line two",
		ts(recent) + " recent line",
		"",
	}, "\n"))

	got := string(pruneOldLines(data, cutoff))
	if strings.Contains(got, "old line") {
		t.Errorf("old lines were not pruned:\n%s", got)
	}
	if !strings.Contains(got, "recent line") {
		t.Errorf("recent line was dropped:\n%s", got)
	}
}

func TestPruneOldLines_KeepsUntimedContinuationWithItsEntry(t *testing.T) {
	now := time.Now()
	old := now.Add(-48 * time.Hour)
	recent := now.Add(-2 * time.Hour)
	cutoff := now.Add(-logRetention)

	data := []byte(strings.Join([]string{
		ts(old) + " old panic:",
		"  goroutine 1 [running]:", // untimed continuation of the OLD entry → drop
		ts(recent) + " recent panic:",
		"  goroutine 2 [running]:", // untimed continuation of the RECENT entry → keep
		"",
	}, "\n"))

	got := string(pruneOldLines(data, cutoff))
	if strings.Contains(got, "goroutine 1") {
		t.Errorf("old continuation was kept:\n%s", got)
	}
	if !strings.Contains(got, "goroutine 2") {
		t.Errorf("recent continuation was dropped:\n%s", got)
	}
}

func TestPruneOldLines_AllRecentUnchanged(t *testing.T) {
	now := time.Now()
	data := []byte(ts(now.Add(-1*time.Hour)) + " a\n" + ts(now) + " b\n")
	got := pruneOldLines(data, now.Add(-logRetention))
	if !bytes.Equal(got, data) {
		t.Errorf("recent-only data was altered:\n got=%q\nwant=%q", got, data)
	}
}

// End-to-end: prune() rewrites the file in place (same path), dropping aged
// lines, and the file remains writable afterwards.
func TestRotatingLog_PruneRewritesInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	now := time.Now()
	initial := ts(now.Add(-48*time.Hour)) + " ancient\n" + ts(now.Add(-30*time.Minute)) + " fresh\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rl := &rotatingLog{f: f, path: path, maxAge: logRetention}

	rl.prune()

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(after), "ancient") {
		t.Errorf("ancient line survived prune:\n%s", after)
	}
	if !strings.Contains(string(after), "fresh") {
		t.Errorf("fresh line lost in prune:\n%s", after)
	}
	// Still writable, and the new write appends after the kept content.
	if _, err := rl.Write([]byte(fmt.Sprintf("%s appended\n", ts(now)))); err != nil {
		t.Fatalf("write after prune: %v", err)
	}
	after2, _ := os.ReadFile(path)
	if !strings.Contains(string(after2), "appended") || !strings.Contains(string(after2), "fresh") {
		t.Errorf("post-prune append corrupted file:\n%s", after2)
	}
}
