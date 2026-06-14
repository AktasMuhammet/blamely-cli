package report

import (
	"strings"
	"testing"

	"github.com/blamely/blamely/internal/gitnotes"
)

// leaderMap collapses the leaderboard into name->lines for easy assertions.

func TestBuildLeaders_HumanDeletionOnly(t *testing.T) {
	// A human deleted 12 lines and nothing else — the leaderboard must list the
	// human, not "no contributors".
	note := &gitnotes.Note{Totals: gitnotes.Totals{
		DeletedLines: 12, AIDeletedLines: 0,
	}}
	got, n := buildLeaders(note, 12 /*added+deleted*/, "alice")
	if n != 1 || len(got) != 1 {
		t.Fatalf("want 1 contributor, got %d", len(got))
	}
	if got[0].Name != "alice" || got[0].Lines != 12 || got[0].IsModel {
		t.Errorf("want alice/12/human, got %+v", got[0])
	}
	if got[0].Pct != "100.0" {
		t.Errorf("want 100.0%%, got %s", got[0].Pct)
	}
}

func TestBuildLeaders_AIDeletionOnly(t *testing.T) {
	// AI removed 20 lines, added none, no per-model data → a generic "AI" entry.
	note := &gitnotes.Note{Totals: gitnotes.Totals{
		DeletedLines: 20, AIDeletedLines: 20,
	}}
	got, _ := buildLeaders(note, 20, "bob")
	if len(got) != 1 || got[0].Name != "AI" || got[0].Lines != 20 || !got[0].IsModel {
		t.Fatalf("want one AI/20 model entry, got %+v", got)
	}
}

func TestBuildLeaders_MixedCountsDeletions(t *testing.T) {
	// AI added 2 + deleted 80; human deleted 53 (like commit f977f65). The AI
	// deletions fold into the model, and the human shows from deletions alone.
	note := &gitnotes.Note{Totals: gitnotes.Totals{
		AILines: 2, HumanLines: 0,
		DeletedLines: 133, AIDeletedLines: 80,
		Models: map[string]int{"claude-opus-4-8": 2},
	}}
	got, n := buildLeaders(note, 2+133, "alice")
	if n != 2 {
		t.Fatalf("want 2 contributors, got %d", n)
	}
	// Rank 1 = the model with 82 (2 added + 80 deleted).
	if got[0].Name != "claude-opus-4-8" || got[0].Lines != 82 {
		t.Errorf("rank1 want claude-opus-4-8/82, got %+v", got[0])
	}
	// Rank 2 = human with 53 (deletions).
	if got[1].Name != "alice" || got[1].Lines != 53 {
		t.Errorf("rank2 want alice/53, got %+v", got[1])
	}
	if got[0].Pct != "60.7" || got[1].Pct != "39.3" {
		t.Errorf("want 60.7 / 39.3, got %s / %s", got[0].Pct, got[1].Pct)
	}
}

func TestBuildLeaders_MultiModelDeletionShare(t *testing.T) {
	// Two models split the AI deletions proportionally to their added lines; the
	// total AI-deleted count is preserved (no rounding loss).
	note := &gitnotes.Note{Totals: gitnotes.Totals{
		AILines: 30, DeletedLines: 10, AIDeletedLines: 10,
		Models: map[string]int{"a": 20, "b": 10},
	}}
	got, _ := buildLeaders(note, 40, "x")
	sum := 0
	for _, l := range got {
		sum += l.Lines
	}
	if sum != 40 { // 30 added + 10 deleted, all AI
		t.Errorf("want total 40 lines across models, got %d (%+v)", sum, got)
	}
}

func TestToolsBody_ShowsAcceptanceAndTokens(t *testing.T) {
	model := "claude-opus-4-8"
	note := &gitnotes.Note{ByTool: map[string]gitnotes.Tool{
		"claude": {Lines: 2, SuggestedLines: 43, AcceptedLines: 2, Model: &model,
			Tokens: &gitnotes.Tokens{Input: 1544, Output: 233, CacheRead: 47101, CacheWrite: 26987}},
	}}
	if !hasAITools(note) {
		t.Fatal("hasAITools should be true")
	}
	body := strings.Join(toolsBody(note), "\n")
	for _, want := range []string{"claude", model, "kept 2/43 (4%)", "tokens"} {
		if !strings.Contains(body, want) {
			t.Errorf("Tools body missing %q\n%s", want, body)
		}
	}
}

func TestToolsBody_NoAITools(t *testing.T) {
	note := &gitnotes.Note{ByTool: map[string]gitnotes.Tool{}}
	if hasAITools(note) {
		t.Error("hasAITools should be false for human-only commit")
	}
}

func TestBuildTools(t *testing.T) {
	model := "claude-opus-4-8"
	note := &gitnotes.Note{ByTool: map[string]gitnotes.Tool{
		"claude": {Lines: 40, SuggestedLines: 50, AcceptedLines: 40, Model: &model,
			Tokens: &gitnotes.Tokens{Input: 1000, Output: 200, CacheRead: 5000, CacheWrite: 0}},
		"cursor": {Lines: 10, SuggestedLines: 0}, // no acceptance, no tokens
	}}
	tools := buildTools(note)
	if len(tools) != 2 {
		t.Fatalf("want 2 tools, got %d", len(tools))
	}
	c := tools[0]
	if c.Name != "claude" || c.Lines != 40 || c.Model != model {
		t.Errorf("claude row wrong: %+v", c)
	}
	if c.Color != "#d97757" || c.Icon == "" {
		t.Errorf("claude should carry its brand color + icon, got color=%q icon-empty=%v", c.Color, c.Icon == "")
	}
	if !c.HasAccept || c.AcceptPct != 80 || c.Kept != 40 || c.Suggested != 50 {
		t.Errorf("acceptance wrong: %+v", c)
	}
	if !c.HasTokens || c.TokIn == "" {
		t.Errorf("tokens should be present: %+v", c)
	}
	if c.WidthPct != 100 { // busiest tool
		t.Errorf("busiest tool width = %v, want 100", c.WidthPct)
	}
	cur := tools[1]
	if cur.HasAccept || cur.HasTokens {
		t.Errorf("cursor row should have no acceptance/tokens: %+v", cur)
	}
	if cur.WidthPct != 25 { // 10/40
		t.Errorf("cursor width = %v, want 25", cur.WidthPct)
	}
}

func TestBuildTools_NoTools(t *testing.T) {
	if got := buildTools(&gitnotes.Note{ByTool: map[string]gitnotes.Tool{}}); got != nil {
		t.Errorf("want nil for no AI tools, got %v", got)
	}
}

func TestToolForModel(t *testing.T) {
	cases := map[string]string{
		"claude-opus-4-8": "claude", "claude-sonnet-4-6": "claude",
		"gemini-3-flash": "gemini", "composer-2.5": "cursor",
		"gpt-4o": "codex", "o3-mini": "codex", "copilot-gpt-5": "copilot",
		"grok-2": "", "llama-3": "", "": "",
	}
	for model, want := range cases {
		if got := toolForModel(model); got != want {
			t.Errorf("toolForModel(%q) = %q, want %q", model, got, want)
		}
	}
}

// TestBuildLeaders_ToolFallbackWhenNoModel reproduces the Antigravity case: a
// tool contributed lines but its model wasn't resolved (Totals.Models empty).
// The leaderboard must still list the AI — by tool name, with its glyph — not
// drop it and show only the human.
func TestBuildLeaders_ToolFallbackWhenNoModel(t *testing.T) {
	note := &gitnotes.Note{
		Totals: gitnotes.Totals{AILines: 1119, HumanLines: 5}, // Models is nil
		ByTool: map[string]gitnotes.Tool{"gemini": {Lines: 1119}},
	}
	got, n := buildLeaders(note, 1124, "alice")
	if n != 2 {
		t.Fatalf("want 2 contributors (gemini + human), got %d: %+v", n, got)
	}
	if got[0].Name != "gemini" || got[0].Lines != 1119 || !got[0].IsModel {
		t.Errorf("rank1 should be the gemini tool with 1119 lines, got %+v", got[0])
	}
	if got[0].Icon == "" || got[0].Color == "" {
		t.Errorf("gemini entry should carry its glyph+color, got icon-empty=%v color=%q", got[0].Icon == "", got[0].Color)
	}
}

// TestBuildLeaders_ModelLabelWhenKnown keeps the model label when by_tool has it.
func TestBuildLeaders_ModelLabelWhenKnown(t *testing.T) {
	model := "claude-sonnet-4-6"
	note := &gitnotes.Note{
		Totals: gitnotes.Totals{AILines: 195}, // Models nil → by_tool fallback
		ByTool: map[string]gitnotes.Tool{"gemini": {Lines: 195, Model: &model}},
	}
	got, _ := buildLeaders(note, 195, "x")
	if len(got) != 1 || got[0].Name != model {
		t.Fatalf("want one entry labeled %q, got %+v", model, got)
	}
}
