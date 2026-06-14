package report

import (
	"strings"
	"testing"
	"time"

	"github.com/blamely/blamely/internal/gitnotes"
)

// ptr returns a pointer to s, used when building RangeEntry.GenType / Tool.
func ptr(s string) *string { return &s }

// ── toolGenType ───────────────────────────────────────────────────────────────

func TestToolGenType_ReturnsEmptyWhenNoFiles(t *testing.T) {
	note := &gitnotes.Note{Files: nil}
	if got := toolGenType(note, "claude"); got != "" {
		t.Errorf("want empty string, got %q", got)
	}
}

func TestToolGenType_ReturnsEmptyWhenToolNotPresent(t *testing.T) {
	chat := "chat"
	note := &gitnotes.Note{
		Files: []gitnotes.FileEntry{
			{Lines: []gitnotes.RangeEntry{
				{Start: 1, End: 5, Tool: "cursor", GenType: &chat},
			}},
		},
	}
	if got := toolGenType(note, "claude"); got != "" {
		t.Errorf("tool not present should return empty, got %q", got)
	}
}

func TestToolGenType_ReturnsDominantGenType(t *testing.T) {
	chat := "chat"
	completion := "completion"
	note := &gitnotes.Note{
		Files: []gitnotes.FileEntry{
			{Lines: []gitnotes.RangeEntry{
				// 10 lines of chat
				{Start: 1, End: 10, Tool: "claude", GenType: &chat},
				// 3 lines of completion
				{Start: 11, End: 13, Tool: "claude", GenType: &completion},
			}},
		},
	}
	if got := toolGenType(note, "claude"); got != "chat" {
		t.Errorf("dominant gen_type = %q, want chat", got)
	}
}

func TestToolGenType_IgnoresNilGenType(t *testing.T) {
	chat := "chat"
	note := &gitnotes.Note{
		Files: []gitnotes.FileEntry{
			{Lines: []gitnotes.RangeEntry{
				{Start: 1, End: 5, Tool: "claude", GenType: nil},   // no gen_type → skip
				{Start: 6, End: 8, Tool: "claude", GenType: &chat}, // 3 lines of chat
			}},
		},
	}
	if got := toolGenType(note, "claude"); got != "chat" {
		t.Errorf("want chat, got %q", got)
	}
}

func TestToolGenType_MultiFile(t *testing.T) {
	cli := "cli"
	chat := "chat"
	note := &gitnotes.Note{
		Files: []gitnotes.FileEntry{
			{Lines: []gitnotes.RangeEntry{
				{Start: 1, End: 2, Tool: "codex", GenType: &cli}, // 2 lines
			}},
			{Lines: []gitnotes.RangeEntry{
				{Start: 1, End: 1, Tool: "codex", GenType: &chat}, // 1 line
			}},
		},
	}
	if got := toolGenType(note, "codex"); got != "cli" {
		t.Errorf("want cli (dominant across files), got %q", got)
	}
}

// ── legacyToolGenType ─────────────────────────────────────────────────────────

func TestLegacyToolGenType(t *testing.T) {
	cases := []struct {
		tool string
		want string
	}{
		{"codex", "cli"},
		{"copilot", "completion"},
		{"claude", "chat"},
		{"cursor", "chat"},
		{"gemini", "chat"},
		{"unknown", "chat"},
		{"", "chat"},
	}
	for _, c := range cases {
		if got := legacyToolGenType(c.tool); got != c.want {
			t.Errorf("legacyToolGenType(%q) = %q, want %q", c.tool, got, c.want)
		}
	}
}

// ── fileToolBreakdown ─────────────────────────────────────────────────────────

func TestFileToolBreakdown_Empty(t *testing.T) {
	f := gitnotes.FileEntry{}
	if got := fileToolBreakdown(f); got != "" {
		t.Errorf("empty file should produce empty breakdown, got %q", got)
	}
}

func TestFileToolBreakdown_SingleTool(t *testing.T) {
	f := gitnotes.FileEntry{
		Lines: []gitnotes.RangeEntry{
			{Start: 1, End: 5, Type: "add", Tool: "claude"},
		},
	}
	got := fileToolBreakdown(f)
	if !strings.Contains(got, "claude 5") {
		t.Errorf("expected 'claude 5' in breakdown, got %q", got)
	}
}

func TestFileToolBreakdown_MultiTool(t *testing.T) {
	f := gitnotes.FileEntry{
		Lines: []gitnotes.RangeEntry{
			{Start: 1, End: 3, Type: "add", Tool: "claude"},  // 3
			{Start: 4, End: 5, Type: "add", Tool: "copilot"}, // 2
		},
	}
	got := fileToolBreakdown(f)
	if !strings.Contains(got, "claude 3") {
		t.Errorf("expected 'claude 3' in %q", got)
	}
	if !strings.Contains(got, "copilot 2") {
		t.Errorf("expected 'copilot 2' in %q", got)
	}
	if !strings.Contains(got, " · ") {
		t.Errorf("multiple tools should be separated by ' · ': %q", got)
	}
}

func TestFileToolBreakdown_SkipsDeletes(t *testing.T) {
	f := gitnotes.FileEntry{
		Lines: []gitnotes.RangeEntry{
			{Start: 1, End: 3, Type: "delete", Tool: "claude"},
			{Start: 1, End: 2, Type: "add", Tool: "cursor"},
		},
	}
	got := fileToolBreakdown(f)
	if strings.Contains(got, "claude") {
		t.Errorf("delete-type lines should not appear in breakdown: %q", got)
	}
	if !strings.Contains(got, "cursor 2") {
		t.Errorf("expected 'cursor 2' in %q", got)
	}
}

func TestFileToolBreakdown_SkipsEmptyTool(t *testing.T) {
	f := gitnotes.FileEntry{
		Lines: []gitnotes.RangeEntry{
			{Start: 1, End: 3, Type: "add", Tool: ""},
			{Start: 4, End: 5, Type: "add", Tool: "claude"},
		},
	}
	got := fileToolBreakdown(f)
	if strings.HasPrefix(got, " ·") {
		t.Errorf("empty-tool lines should not produce leading separator: %q", got)
	}
	if !strings.Contains(got, "claude 2") {
		t.Errorf("expected 'claude 2' in %q", got)
	}
}

// ── humanDuration ─────────────────────────────────────────────────────────────

func TestHumanDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "30s ago"},
		{59 * time.Second, "59s ago"},
		{1 * time.Minute, "1m ago"},
		{45 * time.Minute, "45m ago"},
		{59*time.Minute + 59*time.Second, "59m ago"},
		{1 * time.Hour, "1h ago"},
		{23 * time.Hour, "23h ago"},
		{24 * time.Hour, "1d ago"},
		{72 * time.Hour, "3d ago"},
	}
	for _, c := range cases {
		if got := humanDuration(c.d); got != c.want {
			t.Errorf("humanDuration(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// ── formatK ───────────────────────────────────────────────────────────────────

func TestFormatK(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0"},
		{1, "1"},
		{999, "999"},
		{1000, "1.0k"},
		{1500, "1.5k"},
		{999999, "1000.0k"},
		{1000000, "1.0M"},
		{1500000, "1.5M"},
		{2345678, "2.3M"},
	}
	for _, c := range cases {
		if got := formatK(c.n); got != c.want {
			t.Errorf("formatK(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

// ── renderStats ───────────────────────────────────────────────────────────────

func buildTestNote() *gitnotes.Note {
	chat := "chat"
	model := "claude-opus-4-7"
	return &gitnotes.Note{
		Commit:  "deadbeef1234",
		Branch:  "main",
		Message: "add feature",
		Totals: gitnotes.Totals{
			AILines:      10,
			HumanLines:   3,
			DeletedLines: 2,
			Tokens:       &gitnotes.Tokens{Input: 1000, Output: 200, CacheRead: 50, CacheWrite: 10},
		},
		ByTool: map[string]gitnotes.Tool{
			"claude": {Lines: 10, Model: &model},
		},
		ByGenType: gitnotes.ByGenType{Chat: 10, Human: 3},
		Files: []gitnotes.FileEntry{
			{
				Path:  "main.go",
				Added: 10,
				Lines: []gitnotes.RangeEntry{
					{Start: 1, End: 10, Type: "add", Tool: "claude", GenType: &chat},
				},
			},
		},
	}
}

func TestRenderStats_ContainsCommitHeader(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	note := buildTestNote()
	meta := commitMeta_{"sha": "deadbeef123456", "subject": "add feature", "author": "dev@test.com", "date": ""}
	var buf strings.Builder
	renderStats(&buf, note, meta, 0)
	out := buf.String()
	if !strings.Contains(out, "deadbeef1234") {
		t.Errorf("output should contain short SHA, got:\n%s", out)
	}
	if !strings.Contains(out, "add feature") {
		t.Errorf("output should contain commit subject, got:\n%s", out)
	}
}

func TestRenderStats_ContainsChangesSection(t *testing.T) {
	note := buildTestNote()
	meta := commitMeta_{"sha": "abc", "subject": "test", "author": "", "date": ""}
	var buf strings.Builder
	renderStats(&buf, note, meta, 0)
	out := buf.String()
	if !strings.Contains(out, "Changes") {
		t.Errorf("output should contain Changes section:\n%s", out)
	}
	if !strings.Contains(out, "13") {
		t.Errorf("output should contain added count (10+3=13):\n%s", out)
	}
	if !strings.Contains(out, "2") {
		t.Errorf("output should contain deleted count:\n%s", out)
	}
}

func TestRenderStats_ContainsAIAttribution(t *testing.T) {
	note := buildTestNote()
	meta := commitMeta_{"sha": "abc", "subject": "test", "author": "", "date": ""}
	var buf strings.Builder
	renderStats(&buf, note, meta, 0)
	out := buf.String()
	if !strings.Contains(out, "Attribution") {
		t.Errorf("output should contain AI attribution section:\n%s", out)
	}
	if !strings.Contains(out, "claude") {
		t.Errorf("output should mention claude:\n%s", out)
	}
	if !strings.Contains(out, "10") {
		t.Errorf("output should include claude line count:\n%s", out)
	}
}

func TestRenderStats_ContainsGenerationSection(t *testing.T) {
	note := buildTestNote()
	meta := commitMeta_{"sha": "abc", "subject": "test", "author": "", "date": ""}
	var buf strings.Builder
	renderStats(&buf, note, meta, 0)
	out := buf.String()
	if !strings.Contains(out, "Generation") {
		t.Errorf("output should contain Generation section:\n%s", out)
	}
	if !strings.Contains(out, "chat") {
		t.Errorf("output should mention chat gen type:\n%s", out)
	}
}

func TestRenderStats_ContainsFilesSection(t *testing.T) {
	note := buildTestNote()
	meta := commitMeta_{"sha": "abc", "subject": "test", "author": "", "date": ""}
	var buf strings.Builder
	renderStats(&buf, note, meta, 0)
	out := buf.String()
	if !strings.Contains(out, "Files") {
		t.Errorf("output should contain Files section:\n%s", out)
	}
	if !strings.Contains(out, "main.go") {
		t.Errorf("output should contain main.go:\n%s", out)
	}
}

func TestRenderStats_ContainsTokensSection(t *testing.T) {
	note := buildTestNote()
	meta := commitMeta_{"sha": "abc", "subject": "test", "author": "", "date": ""}
	var buf strings.Builder
	renderStats(&buf, note, meta, 0)
	out := buf.String()
	if !strings.Contains(out, "Tokens") {
		t.Errorf("output should contain Tokens section:\n%s", out)
	}
	if !strings.Contains(out, "1.0k") {
		t.Errorf("output should format input tokens as 1.0k:\n%s", out)
	}
}

func TestRenderStats_CodingTime(t *testing.T) {
	note := buildTestNote()
	meta := commitMeta_{"sha": "abc", "subject": "test", "author": "", "date": ""}
	var buf strings.Builder
	sessionNanos := int64(45 * 60 * 1e9) // 45 minutes
	renderStats(&buf, note, meta, sessionNanos)
	out := buf.String()
	if !strings.Contains(out, "Coding") {
		t.Errorf("output should contain Coding time section:\n%s", out)
	}
	if !strings.Contains(out, "45 min") {
		t.Errorf("output should mention ~45 min:\n%s", out)
	}
}

func TestRenderStats_NoTokensSection_WhenNil(t *testing.T) {
	note := buildTestNote()
	note.Totals.Tokens = nil
	meta := commitMeta_{"sha": "abc", "subject": "test", "author": "", "date": ""}
	var buf strings.Builder
	renderStats(&buf, note, meta, 0)
	out := buf.String()
	if strings.Contains(out, "Tokens") {
		t.Errorf("Tokens section should be omitted when nil:\n%s", out)
	}
}

func TestRenderStats_AuthorLine(t *testing.T) {
	note := buildTestNote()
	meta := commitMeta_{"sha": "abc", "subject": "test", "author": "dev@example.com", "date": ""}
	var buf strings.Builder
	renderStats(&buf, note, meta, 0)
	out := buf.String()
	if !strings.Contains(out, "dev@example.com") {
		t.Errorf("output should contain author:\n%s", out)
	}
}
