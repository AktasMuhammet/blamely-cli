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

func TestCodexBaseDirs_UnionAlwaysIncludesDefault(t *testing.T) {
	home := fakeHome(t)
	t.Setenv("CODEX_HOME", "")

	// Default only.
	got := CodexBaseDirs()
	want := filepath.Join(home, ".codex")
	if len(got) != 1 || got[0] != want {
		t.Fatalf("default-only union = %v, want [%s]", got, want)
	}

	// Env dir is ADDED, default kept.
	corp := filepath.Join(home, ".codex-corp", "codex-config")
	t.Setenv("CODEX_HOME", corp)
	got = CodexBaseDirs()
	if len(got) != 2 || got[0] != want || got[1] != filepath.Clean(corp) {
		t.Fatalf("env union = %v, want [%s %s]", got, want, corp)
	}
}

func TestClaudeBaseDirs_ConfigDirsAddedAndDeduped(t *testing.T) {
	home := fakeHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	// Persist an extra dir via config; the default must still lead the union.
	extra := filepath.Join(home, ".claude-corp", "claude-config")
	cfg := LoadConfig()
	cfg.AddToList("claude_config_dirs", extra)
	if _, err := SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}

	// Set the env to the SAME dir → must be de-duplicated.
	t.Setenv("CLAUDE_CONFIG_DIR", extra)
	got := ClaudeBaseDirs()
	want := []string{filepath.Join(home, ".claude"), filepath.Clean(extra)}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("union = %v, want %v (default first, extra once)", got, want)
	}

	// Derived paths hang off each base.
	sessions := ClaudeProjectsDirs()
	if sessions[len(sessions)-1] != filepath.Join(extra, "projects") {
		t.Errorf("ClaudeProjectsDirs missing custom projects dir: %v", sessions)
	}
}

func TestCaptureToolDirsFromEnv_PersistsCustomOnly(t *testing.T) {
	home := fakeHome(t)
	// Env set to the DEFAULT must not be persisted (redundant).
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude-corp", "cfg"))
	if err := CaptureToolDirsFromEnv(); err != nil {
		t.Fatal(err)
	}
	cfg := LoadConfig()
	if len(cfg.Tools.CodexHomes) != 0 {
		t.Errorf("default CODEX_HOME must not be persisted; got %v", cfg.Tools.CodexHomes)
	}
	if len(cfg.Tools.ClaudeConfigDirs) != 1 {
		t.Errorf("custom CLAUDE_CONFIG_DIR must be persisted; got %v", cfg.Tools.ClaudeConfigDirs)
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
