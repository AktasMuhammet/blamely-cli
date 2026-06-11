package report

import (
	"strings"
	"testing"

	"github.com/blamely/blamely/internal/gitnotes"
)

// makeNote builds a minimal Note for bar rendering tests.
func makeNote(aiLines, humanLines int, tools map[string]gitnotes.Tool) *gitnotes.Note {
	byTool := tools
	if byTool == nil {
		byTool = map[string]gitnotes.Tool{}
	}
	if aiLines > 0 && byTool["claude"].Lines == 0 && len(byTool) == 0 {
		byTool["claude"] = gitnotes.Tool{Lines: aiLines}
	}
	return &gitnotes.Note{
		Commit:  "abc123",
		Totals:  gitnotes.Totals{AILines: aiLines, HumanLines: humanLines},
		ByTool:  byTool,
	}
}

func TestRenderBar_NoChanges(t *testing.T) {
	var buf strings.Builder
	t.Setenv("NO_COLOR", "1")
	RenderBar(&buf, makeNote(0, 0, nil), 40)
	if !strings.Contains(buf.String(), "no changes") {
		t.Errorf("empty-commit bar should say 'no changes': got %q", buf.String())
	}
}

func TestRenderBar_DeletionOnly_HundredPercentHuman(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf strings.Builder
	note := &gitnotes.Note{
		Totals: gitnotes.Totals{AILines: 0, HumanLines: 0, DeletedLines: 7},
		ByTool: map[string]gitnotes.Tool{},
	}
	RenderBar(&buf, note, 20)
	out := buf.String()
	if !strings.Contains(out, "Human 100%") {
		t.Errorf("deletion-only bar should read 'Human 100%%', got: %q", out)
	}
	if !strings.Contains(out, "(7 deleted)") {
		t.Errorf("deletion-only bar should mention the deleted count, got: %q", out)
	}
	if !strings.Contains(out, strings.Repeat("-", 20)) {
		t.Errorf("deletion-only bar should be full-width human, got: %q", out)
	}
}

func TestRenderBar_AdditionsAndDeletions_NoteAppended(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf strings.Builder
	note := &gitnotes.Note{
		Totals: gitnotes.Totals{AILines: 3, HumanLines: 1, DeletedLines: 2},
		ByTool: map[string]gitnotes.Tool{
			"claude": {Lines: 3},
			"human":  {Lines: 1},
		},
	}
	RenderBar(&buf, note, 20)
	out := buf.String()
	if !strings.Contains(out, "AI 75%") {
		t.Errorf("expected 'AI 75%%' in bar, got: %q", out)
	}
	if !strings.Contains(out, "Deleted: 2 lines (treated as 100% human)") {
		t.Errorf("expected deletion side-note, got: %q", out)
	}
}

// TestRenderBar_AdditionsAndDeletions_AIDeletedNote verifies that when
// ai_deleted_lines is set, the deletion side-note breaks the count down by
// AI vs Human instead of always saying "treated as 100% human".
func TestRenderBar_AdditionsAndDeletions_AIDeletedNote(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf strings.Builder
	note := &gitnotes.Note{
		Totals: gitnotes.Totals{AILines: 3, HumanLines: 1, DeletedLines: 5, AIDeletedLines: 4},
		ByTool: map[string]gitnotes.Tool{
			"claude": {Lines: 3},
			"human":  {Lines: 1},
		},
	}
	RenderBar(&buf, note, 20)
	out := buf.String()
	if !strings.Contains(out, "Deleted: 5 lines (4 AI / 1 Human)") {
		t.Errorf("expected AI/Human deletion breakdown, got: %q", out)
	}
}

// TestRenderBar_DeletionOnly_AllAI covers the bug report where a commit
// deleted 19 lines, all of them AI-attributed (ai_deleted_lines == deleted_lines),
// but the bar still read "Human 100%". It should now read "AI 100%" with a
// fully-filled bar.
func TestRenderBar_DeletionOnly_AllAI(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf strings.Builder
	note := &gitnotes.Note{
		Totals: gitnotes.Totals{AILines: 0, HumanLines: 0, DeletedLines: 19, AIDeletedLines: 19},
		ByTool: map[string]gitnotes.Tool{},
	}
	RenderBar(&buf, note, 20)
	out := buf.String()
	if !strings.Contains(out, "AI 100% (19 deleted)") {
		t.Errorf("expected 'AI 100%% (19 deleted)', got: %q", out)
	}
	if !strings.Contains(out, "Human 0% (0 deleted)") {
		t.Errorf("expected 'Human 0%% (0 deleted)', got: %q", out)
	}
	if !strings.Contains(out, strings.Repeat("#", 20)) {
		t.Errorf("deletion-only all-AI bar should be full-width AI, got: %q", out)
	}
}

// TestRenderBar_DeletionOnly_PartialAI covers a deletion-only commit where
// some but not all deleted lines are AI-attributed.
func TestRenderBar_DeletionOnly_PartialAI(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf strings.Builder
	note := &gitnotes.Note{
		Totals: gitnotes.Totals{AILines: 0, HumanLines: 0, DeletedLines: 10, AIDeletedLines: 7},
		ByTool: map[string]gitnotes.Tool{},
	}
	RenderBar(&buf, note, 20)
	out := buf.String()
	if !strings.Contains(out, "AI 70% (7 deleted)") {
		t.Errorf("expected 'AI 70%% (7 deleted)', got: %q", out)
	}
	if !strings.Contains(out, "Human 30% (3 deleted)") {
		t.Errorf("expected 'Human 30%% (3 deleted)', got: %q", out)
	}
}

func TestRenderBar_AllAI(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf strings.Builder
	RenderBar(&buf, makeNote(10, 0, map[string]gitnotes.Tool{
		"claude": {Lines: 10},
	}), 20)
	out := buf.String()
	if !strings.Contains(out, "100%") {
		t.Errorf("all-AI bar should say 100%% AI: %q", out)
	}
	if strings.Contains(out, "Human 0%") {
		// ok — zero human
	}
	// The AI portion should fill the full bar width with '#'
	if !strings.Contains(out, strings.Repeat("#", 20)) {
		t.Errorf("full AI bar should have 20 # chars: %q", out)
	}
}

func TestRenderBar_AllHuman(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf strings.Builder
	RenderBar(&buf, makeNote(0, 5, map[string]gitnotes.Tool{
		"human": {Lines: 5},
	}), 20)
	out := buf.String()
	if !strings.Contains(out, "Human 100%") {
		t.Errorf("all-human bar should say 'Human 100%%': %q", out)
	}
	// Human bar uses '-' chars in NO_COLOR mode
	if !strings.Contains(out, strings.Repeat("-", 20)) {
		t.Errorf("full human bar should have 20 - chars: %q", out)
	}
}

func TestRenderBar_Mixed50_50(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf strings.Builder
	note := makeNote(5, 5, map[string]gitnotes.Tool{
		"claude": {Lines: 5},
		"human":  {Lines: 5},
	})
	RenderBar(&buf, note, 40)
	out := buf.String()
	if !strings.Contains(out, "AI 50%") {
		t.Errorf("50/50 bar should say AI 50%%: %q", out)
	}
	if !strings.Contains(out, "Human 50%") {
		t.Errorf("50/50 bar should say Human 50%%: %q", out)
	}
}

func TestRenderBar_LayoutAILeftHumanRight(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf strings.Builder
	RenderBar(&buf, makeNote(3, 1, map[string]gitnotes.Tool{
		"claude": {Lines: 3},
		"human":  {Lines: 1},
	}), 40)
	out := buf.String()
	// "AI" should appear before the "[" bracket, "Human" after "]"
	aiPos := strings.Index(out, "AI ")
	bracketOpen := strings.Index(out, "[")
	bracketClose := strings.Index(out, "]")
	humanPos := strings.LastIndex(out, "Human")
	if aiPos < 0 || bracketOpen < 0 || bracketClose < 0 || humanPos < 0 {
		t.Fatalf("bar missing expected tokens: %q", out)
	}
	if aiPos > bracketOpen {
		t.Errorf("AI label should be to the LEFT of '[': aiPos=%d bracketOpen=%d", aiPos, bracketOpen)
	}
	if humanPos < bracketClose {
		t.Errorf("Human label should be to the RIGHT of ']': humanPos=%d bracketClose=%d", humanPos, bracketClose)
	}
}

func TestRenderBar_PerToolBreakdown_Claude(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf strings.Builder
	model := "claude-opus-4-7"
	tokens := &gitnotes.Tokens{Input: 1200, Output: 340}
	note := &gitnotes.Note{
		Totals: gitnotes.Totals{AILines: 3, HumanLines: 1},
		ByTool: map[string]gitnotes.Tool{
			"claude": {Lines: 3, Model: &model, Tokens: tokens},
			"human":  {Lines: 1},
		},
	}
	RenderBar(&buf, note, 40)
	out := buf.String()
	if !strings.Contains(out, "claude") {
		t.Errorf("breakdown should mention claude: %q", out)
	}
	if !strings.Contains(out, "claude-opus-4-7") {
		t.Errorf("breakdown should mention model: %q", out)
	}
	if !strings.Contains(out, "1200") {
		t.Errorf("breakdown should mention input tokens: %q", out)
	}
}

func TestRenderBar_PerToolBreakdown_OnlyNonZero(t *testing.T) {
	// Codex and Copilot have 0 lines — they should NOT appear in the breakdown.
	t.Setenv("NO_COLOR", "1")
	var buf strings.Builder
	note := &gitnotes.Note{
		Totals: gitnotes.Totals{AILines: 2, HumanLines: 1},
		ByTool: map[string]gitnotes.Tool{
			"claude":  {Lines: 2},
			"codex":   {Lines: 0},
			"copilot": {Lines: 0},
			"human":   {Lines: 1},
		},
	}
	RenderBar(&buf, note, 40)
	out := buf.String()
	if strings.Contains(out, "codex 0") {
		t.Errorf("zero-line tools should be omitted from breakdown: %q", out)
	}
	if strings.Contains(out, "copilot 0") {
		t.Errorf("zero-line tools should be omitted from breakdown: %q", out)
	}
}

func TestRenderBar_ColorEnabled_NoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if colorEnabled() {
		t.Error("colorEnabled() should return false when NO_COLOR=1")
	}
}

func TestRenderBar_ColorEnabled_Default(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	if !colorEnabled() {
		t.Error("colorEnabled() should return true when NO_COLOR is unset")
	}
}

func TestRenderBar_Width_Default(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf strings.Builder
	note := makeNote(5, 5, map[string]gitnotes.Tool{
		"claude": {Lines: 5}, "human": {Lines: 5},
	})
	// width=0 should use default 40
	RenderBar(&buf, note, 0)
	out := buf.String()
	total := strings.Count(out, "#") + strings.Count(out, "-")
	if total != 40 {
		t.Errorf("default width should produce 40 bar chars, got %d in: %q", total, out)
	}
}
