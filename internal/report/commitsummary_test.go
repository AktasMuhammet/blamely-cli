package report

import (
	"bytes"
	"strings"
	"testing"

	"github.com/blamely/blamely/internal/gitnotes"
)

// TestRenderCommitSummary_DeletionOnlyShowsBar verifies that a deletion-only
// commit still renders the AI/Human progress bar (it used to drop it and print
// only "−N deleted   AI x · Human y").
func TestRenderCommitSummary_DeletionOnlyShowsBar(t *testing.T) {
	// Human deleted 10 lines, AI deleted 6 — no additions at all.
	note := &gitnotes.Note{
		Commit: "deadbeef",
		Totals: gitnotes.Totals{DeletedLines: 16, AIDeletedLines: 6},
	}
	var buf bytes.Buffer
	RenderCommitSummary(&buf, note)
	out := buf.String()

	if !strings.Contains(out, "AI ") || !strings.Contains(out, "%") {
		t.Errorf("deletion-only summary should show an AI%% gauge:\n%s", out)
	}
	// The stack bar uses block glyphs — at least one must be present.
	if !strings.ContainsAny(out, "█░") {
		t.Errorf("deletion-only summary should render a progress bar:\n%s", out)
	}
	if !strings.Contains(out, "−16") || !strings.Contains(out, "deleted") {
		t.Errorf("expected the −16 deleted changes line:\n%s", out)
	}
	// AI share = 6/16 = 37.5% -> rounds to "AI 38%".
	if !strings.Contains(out, "AI 38%") {
		t.Errorf("expected AI 38%% (6 of 16 deleted):\n%s", out)
	}
}

// TestRenderCommitSummary_NoChanges keeps the empty-commit message.
func TestRenderCommitSummary_NoChanges(t *testing.T) {
	var buf bytes.Buffer
	RenderCommitSummary(&buf, &gitnotes.Note{Commit: "abc"})
	if !strings.Contains(buf.String(), "no attributable changes") {
		t.Errorf("want 'no attributable changes', got:\n%s", buf.String())
	}
}
