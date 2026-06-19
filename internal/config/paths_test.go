package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeHome points os.UserHomeDir() at a fresh temp dir for the duration of
// the test. Sets HOME (POSIX) and USERPROFILE (Windows) and clears the
// HOMEDRIVE/HOMEPATH fallback so Go can't pick that up either.
func fakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")
	return home
}

// pathTests is one row per Path-returning function. Each function joins
// $HOME with a known relative suffix; verifying that mapping in one place
// catches accidental rewires (e.g. swapping ".claude" for ".gemini").
var pathTests = []struct {
	name string
	fn   func() (string, error)
	want []string // joined with filepath.Join
}{
	{"BlamelyDir", BlamelyDir, []string{".blamely"}},
	{"DBPath", DBPath, []string{".blamely", "db.sqlite"}},
	{"PortFile", PortFile, []string{".blamely", "daemon.port"}},
	{"StateFile", StateFile, []string{".blamely", "state.json"}},
	{"GitHooksDir", GitHooksDir, []string{".blamely", "git-hooks"}},
	{"LogFile", LogFile, []string{".blamely", "daemon.log"}},
	{"ClaudeSettingsPath", ClaudeSettingsPath, []string{".claude", "settings.json"}},
	{"CodexSessionsDir", CodexSessionsDir, []string{".codex", "sessions"}},
	{"CodexConfigPath", CodexConfigPath, []string{".codex", "config.toml"}},
	{"CursorHooksPath", CursorHooksPath, []string{".cursor", "hooks.json"}},
	{"CopilotHooksDir", CopilotHooksDir, []string{".copilot", "hooks"}},
	{"CopilotBlamelyHookPath", CopilotBlamelyHookPath, []string{".copilot", "hooks", "blamely.json"}},
	{"CopilotSessionStateDir", CopilotSessionStateDir, []string{".copilot", "session-state"}},
	{"GeminiSettingsPath", GeminiSettingsPath, []string{".gemini", "settings.json"}},
}

func TestPaths_AreRelativeToHome(t *testing.T) {
	home := fakeHome(t)
	for _, tc := range pathTests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.fn()
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			want := filepath.Join(append([]string{home}, tc.want...)...)
			if got != want {
				t.Errorf("%s = %q, want %q", tc.name, got, want)
			}
		})
	}
}

func TestHome_ReturnsHomeEnv(t *testing.T) {
	home := fakeHome(t)
	got, err := Home()
	if err != nil {
		t.Fatalf("Home: %v", err)
	}
	if got != home {
		t.Errorf("Home = %q, want %q", got, home)
	}
}

func TestHome_ErrorWhenUnresolvable(t *testing.T) {
	// Strip every env var os.UserHomeDir consults so it cannot resolve.
	// Windows checks USERPROFILE then HOMEDRIVE+HOMEPATH; POSIX checks HOME.
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")
	if _, err := Home(); err == nil {
		t.Error("expected error when no home env vars are set")
	}
}

func TestEnsureBlamelyDir_CreatesDir(t *testing.T) {
	home := fakeHome(t)
	dir, err := EnsureBlamelyDir()
	if err != nil {
		t.Fatalf("EnsureBlamelyDir: %v", err)
	}
	wantDir := filepath.Join(home, ".blamely")
	if dir != wantDir {
		t.Errorf("dir = %q, want %q", dir, wantDir)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !fi.IsDir() {
		t.Errorf("%s is not a directory", dir)
	}
}

func TestEnsureBlamelyDir_Idempotent(t *testing.T) {
	fakeHome(t)
	if _, err := EnsureBlamelyDir(); err != nil {
		t.Fatal(err)
	}
	// Calling twice on an existing dir must not error.
	if _, err := EnsureBlamelyDir(); err != nil {
		t.Fatalf("second call: %v", err)
	}
}

func TestEnsureBlamelyDir_ErrorWhenParentNotWritable(t *testing.T) {
	// MkdirAll succeeds against existing dirs and creates missing ones, so the
	// only way to make it fail is to point HOME at a path that can't be made
	// into a directory. We point HOME at a regular file: ~/.blamely then
	// resolves to <file>/.blamely, which mkdir can't create.
	if runtime.GOOS == "windows" {
		// On Windows the semantics of os.MkdirAll over a file are different and
		// flaky in CI; skipping keeps this assertion meaningful on POSIX where
		// we can rely on it.
		t.Skip("file-as-parent semantics differ on Windows")
	}
	parent := t.TempDir()
	notADir := filepath.Join(parent, "regular-file")
	if err := os.WriteFile(notADir, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", notADir)
	t.Setenv("USERPROFILE", notADir)
	if _, err := EnsureBlamelyDir(); err == nil {
		t.Error("expected error when HOME is a regular file")
	} else if !strings.Contains(err.Error(), "mkdir") {
		t.Errorf("expected mkdir error, got: %v", err)
	}
}
