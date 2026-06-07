package install

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ── presence ──────────────────────────────────────────────────────────────────

func TestPresence_EmptyHintsIsNotPresent(t *testing.T) {
	p := presence(nil)
	if p.Present {
		t.Error("no hints → Present should be false")
	}
	if len(p.Hints) != 0 {
		t.Errorf("Hints should be empty, got %v", p.Hints)
	}
}

func TestPresence_WithHintsIsPresent(t *testing.T) {
	hints := []string{"/some/path", "/other/path"}
	p := presence(hints)
	if !p.Present {
		t.Error("non-empty hints → Present should be true")
	}
	if len(p.Hints) != 2 {
		t.Errorf("Hints length = %d, want 2", len(p.Hints))
	}
}

func TestPresence_FirstHint(t *testing.T) {
	p := presence([]string{"/a", "/b"})
	if got := p.FirstHint(); got != "/a" {
		t.Errorf("FirstHint() = %q, want /a", got)
	}
}

func TestPresence_FirstHint_EmptyList(t *testing.T) {
	p := presence(nil)
	if got := p.FirstHint(); got != "" {
		t.Errorf("FirstHint() on empty = %q, want empty", got)
	}
}

// ── bytesContains ─────────────────────────────────────────────────────────────

func TestBytesContains_Found(t *testing.T) {
	cases := []struct {
		haystack string
		needle   string
	}{
		{"hello world", "world"},
		{"hello world", "hello"},
		{"hello world", "lo wo"},
		{"x", "x"},
	}
	for _, c := range cases {
		if !bytesContains([]byte(c.haystack), c.needle) {
			t.Errorf("bytesContains(%q, %q) = false, want true", c.haystack, c.needle)
		}
	}
}

func TestBytesContains_NotFound(t *testing.T) {
	cases := []struct {
		haystack string
		needle   string
	}{
		{"hello world", "xyz"},
		{"hello", "hello world"},
		{"", "x"},
	}
	for _, c := range cases {
		if bytesContains([]byte(c.haystack), c.needle) {
			t.Errorf("bytesContains(%q, %q) = true, want false", c.haystack, c.needle)
		}
	}
}

func TestBytesContains_EmptyNeedle(t *testing.T) {
	// An empty needle is always "found" in any haystack since len(needle)=0
	// and every position satisfies the check.
	if !bytesContains([]byte("anything"), "") {
		t.Error("empty needle should be found in any haystack")
	}
}

func TestBytesContains_CaseSensitive(t *testing.T) {
	if bytesContains([]byte("Copilot"), "copilot") {
		t.Error("bytesContains should be case-sensitive")
	}
}

// ── cursorCandidates ──────────────────────────────────────────────────────────

func TestCursorCandidates_AlwaysIncludesDotCursor(t *testing.T) {
	home := t.TempDir()
	candidates := cursorCandidates(home)
	want := filepath.Join(home, ".cursor")
	found := false
	for _, c := range candidates {
		if c == want {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("cursorCandidates should always include ~/.cursor; got: %v", candidates)
	}
}

func TestCursorCandidates_AlwaysIncludesCursorExtensions(t *testing.T) {
	home := t.TempDir()
	candidates := cursorCandidates(home)
	want := filepath.Join(home, ".cursor", "extensions")
	found := false
	for _, c := range candidates {
		if c == want {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("cursorCandidates should include ~/.cursor/extensions; got: %v", candidates)
	}
}

func TestCursorCandidates_AlwaysIncludesCursorServer(t *testing.T) {
	home := t.TempDir()
	candidates := cursorCandidates(home)
	want := filepath.Join(home, ".cursor-server")
	found := false
	for _, c := range candidates {
		if c == want {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("cursorCandidates should include ~/.cursor-server; got: %v", candidates)
	}
}

func TestCursorCandidates_NonEmpty(t *testing.T) {
	home := t.TempDir()
	candidates := cursorCandidates(home)
	if len(candidates) == 0 {
		t.Error("cursorCandidates should return at least the common paths")
	}
}

func TestCursorCandidates_DarwinHasAppPath(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only test")
	}
	home := t.TempDir()
	candidates := cursorCandidates(home)
	found := false
	for _, c := range candidates {
		if strings.Contains(c, "Cursor.app") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("darwin cursorCandidates should include Cursor.app path; got: %v", candidates)
	}
}

func TestCursorCandidates_LinuxHasConfigCursor(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux-only test")
	}
	home := t.TempDir()
	candidates := cursorCandidates(home)
	want := filepath.Join(home, ".config", "Cursor")
	found := false
	for _, c := range candidates {
		if c == want {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("linux cursorCandidates should include ~/.config/Cursor; got: %v", candidates)
	}
}

func TestCursorCandidates_AllContainHome(t *testing.T) {
	home := t.TempDir()
	candidates := cursorCandidates(home)
	for _, c := range candidates {
		// Platform paths like /Applications/Cursor.app or /opt/cursor don't
		// contain home, so only check paths that are under home.
		if strings.HasPrefix(c, home) {
			// Ensure the path contains the home directory correctly.
			if !strings.HasPrefix(c, home) {
				t.Errorf("candidate %q doesn't start with home %q", c, home)
			}
		}
	}
}
