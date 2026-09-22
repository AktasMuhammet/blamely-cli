package install

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withUpdateLogHome points ~/.blamely at a temp dir so the test writes its own
// update.log instead of the developer's.
func withUpdateLogHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir reads this on Windows
	if err := os.MkdirAll(filepath.Join(home, ".blamely"), 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(home, ".blamely", "update.log")
}

func TestLogUpdateOutcome(t *testing.T) {
	cases := []struct {
		name     string
		res      UpdateResult
		err      error
		wantLine bool
		contains []string
	}{
		{
			name:     "success records both versions and the channel used",
			res:      UpdateResult{From: "1.8.1", To: "1.8.2", Updated: true, Channel: "beta"},
			wantLine: true,
			contains: []string{"update ok", "1.8.1 -> 1.8.2", "(channel beta)"},
		},
		{
			name:     "failure records the reason and the detail",
			res:      UpdateResult{From: "1.8.1", To: "1.8.2", Reason: "checksum mismatch"},
			err:      errors.New("sha256 of blamely_windows_amd64.zip does not match"),
			wantLine: true,
			contains: []string{"update failed", "1.8.1 -> 1.8.2", "checksum mismatch", "sha256"},
		},
		{
			name:     "failure before the target version is known prints one version",
			res:      UpdateResult{From: "1.8.1", To: "1.8.1", Reason: "not the installed binary"},
			err:      errors.New("run `blamely install` first"),
			wantLine: true,
			contains: []string{"update failed: 1.8.1:", "not the installed binary"},
		},
		{
			// A daemon checks every 24h; "already current" must not write a line
			// a day, or the interesting lines drown.
			name:     "nothing attempted writes nothing",
			res:      UpdateResult{From: "1.8.2", To: "1.8.2"},
			wantLine: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := withUpdateLogHome(t)
			logUpdateOutcome(c.res, c.err)

			b, err := os.ReadFile(path)
			if !c.wantLine {
				if err == nil && len(strings.TrimSpace(string(b))) > 0 {
					t.Fatalf("expected no log line, got %q", b)
				}
				return
			}
			if err != nil {
				t.Fatalf("update.log not written: %v", err)
			}
			got := string(b)
			for _, want := range c.contains {
				if !strings.Contains(got, want) {
					t.Errorf("line %q does not contain %q", strings.TrimSpace(got), want)
				}
			}
			if strings.Count(strings.TrimSpace(got), "\n") != 0 {
				t.Errorf("one attempt must be one line, got %q", got)
			}
		})
	}
}

// The log is a history: attempts accumulate and doctor reads the newest.
func TestUpdateLogAppendsAndLastLine(t *testing.T) {
	withUpdateLogHome(t)
	logUpdateOutcome(UpdateResult{From: "1.8.0", To: "1.8.1", Updated: true}, nil)
	logUpdateOutcome(UpdateResult{From: "1.8.1", To: "1.8.2", Reason: "download failed"},
		errors.New("dial tcp: i/o timeout"))

	last, ok := LastUpdateLogLine()
	if !ok {
		t.Fatal("LastUpdateLogLine found nothing")
	}
	if !strings.Contains(last, "1.8.1 -> 1.8.2") || !strings.Contains(last, "download failed") {
		t.Errorf("last line = %q, want the most recent (failed) attempt", last)
	}
}

// A staged installer's output can be many lines; a log line must stay one line
// and stay readable.
func TestUpdateLogOneLineTruncates(t *testing.T) {
	path := withUpdateLogHome(t)
	logUpdateOutcome(
		UpdateResult{From: "1.8.1", To: "1.8.2", Reason: "post-install step did not finish"},
		errors.New("line one\nline two\n"+strings.Repeat("x", 500)))

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimRight(string(b), "\n")
	if strings.Contains(line, "\n") {
		t.Fatalf("multi-line error was not flattened: %q", line)
	}
	if !strings.Contains(line, "line one line two") {
		t.Errorf("newlines should collapse to spaces, got %q", line)
	}
	if !strings.HasSuffix(line, "...") {
		t.Errorf("an over-long reason should be truncated, got %q", line)
	}
}
