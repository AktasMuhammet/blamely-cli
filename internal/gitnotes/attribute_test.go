package gitnotes

import (
	"database/sql"
	"testing"

	"github.com/blamely/blamely/internal/store"
)

// ---- coversLine ----

func TestCoversLine_Inside(t *testing.T) {
	lines := []store.EditLine{{StartLine: 5, EndLine: 10}}
	for _, n := range []int{5, 7, 10} {
		if !coversLine(lines, n) {
			t.Errorf("line %d should be covered by [5,10]", n)
		}
	}
}

func TestCoversLine_Outside(t *testing.T) {
	lines := []store.EditLine{{StartLine: 5, EndLine: 10}}
	for _, n := range []int{1, 4, 11, 100} {
		if coversLine(lines, n) {
			t.Errorf("line %d should NOT be covered by [5,10]", n)
		}
	}
}

func TestCoversLine_MultipleRanges(t *testing.T) {
	lines := []store.EditLine{
		{StartLine: 1, EndLine: 3},
		{StartLine: 8, EndLine: 10},
	}
	if !coversLine(lines, 2) {
		t.Error("line 2 should be covered by first range")
	}
	if !coversLine(lines, 9) {
		t.Error("line 9 should be covered by second range")
	}
	if coversLine(lines, 5) {
		t.Error("line 5 is in the gap, should NOT be covered")
	}
}

func TestCoversLine_Empty(t *testing.T) {
	if coversLine(nil, 1) {
		t.Error("empty lines should cover nothing")
	}
}

// ---- defaultConf / confidenceFor ----

func TestDefaultConf(t *testing.T) {
	if defaultConf(store.ToolCopilot) != store.ConfidenceLow {
		t.Error("copilot should have low confidence")
	}
	for _, tool := range []store.Tool{store.ToolClaude, store.ToolCursor, store.ToolCodex, store.ToolHuman} {
		if defaultConf(tool) != store.ConfidenceHigh {
			t.Errorf("%s should have high confidence", tool)
		}
	}
}

func TestConfidenceFor_NilEdit(t *testing.T) {
	// When there's no edit, defaults apply.
	conf := confidenceFor(store.ToolClaude, nil)
	if conf != store.ConfidenceHigh {
		t.Errorf("want high, got %s", conf)
	}
	conf = confidenceFor(store.ToolCopilot, nil)
	if conf != store.ConfidenceLow {
		t.Errorf("want low for copilot, got %s", conf)
	}
}

func TestConfidenceFor_EditConfidence(t *testing.T) {
	e := &store.Edit{Confidence: store.ConfidenceMedium}
	if got := confidenceFor(store.ToolCursor, e); got != store.ConfidenceMedium {
		t.Errorf("want medium from edit, got %s", got)
	}
}

// ---- hasUsage / nullInt64 ----

func TestHasUsage(t *testing.T) {
	empty := &store.Edit{}
	if hasUsage(empty) {
		t.Error("empty edit should have no usage")
	}
	withInput := &store.Edit{InputTokens: sql.NullInt64{Valid: true, Int64: 100}}
	if !hasUsage(withInput) {
		t.Error("edit with input_tokens should have usage")
	}
}

func TestNullInt64(t *testing.T) {
	if nullInt64(sql.NullInt64{Valid: false}) != 0 {
		t.Error("invalid NullInt64 should return 0")
	}
	if nullInt64(sql.NullInt64{Valid: true, Int64: 42}) != 42 {
		t.Error("valid NullInt64 should return its value")
	}
}

// ---- buildNote: per-line shape, deletions, suggested vs accepted, zero by_tool ----

func TestCollapseToRanges(t *testing.T) {
	chat := "chat"
	human := "human"
	mk := func(line int, typ, author, tool string, gt *string) LineEntry {
		return LineEntry{Line: line, Type: typ, AuthorType: author, Tool: tool, GenType: gt}
	}
	in := []LineEntry{
		// Contiguous AI/claude/chat 1..3 → one range.
		mk(1, "add", "AI", "claude", &chat),
		mk(2, "add", "AI", "claude", &chat),
		mk(3, "add", "AI", "claude", &chat),
		// Human 4..5 → separate range (attribution changes).
		mk(4, "add", "Human", "", &human),
		mk(5, "add", "Human", "", &human),
		// Gap at 6 (no entry); AI again at 7 → new range despite same attribution.
		mk(7, "add", "AI", "claude", &chat),
		// Delete at line 3 must NOT merge with the add range at 3 (Type differs).
		mk(3, "delete", "Human", "", nil),
	}
	got := collapseToRanges(in)
	want := []RangeEntry{
		{Start: 1, End: 3, Type: "add", AuthorType: "AI", Tool: "claude", GenType: &chat},
		{Start: 3, End: 3, Type: "delete", AuthorType: "Human"},
		{Start: 4, End: 5, Type: "add", AuthorType: "Human", GenType: &human},
		{Start: 7, End: 7, Type: "add", AuthorType: "AI", Tool: "claude", GenType: &chat},
	}
	if len(got) != len(want) {
		t.Fatalf("range count: want %d, got %d (%+v)", len(want), len(got), got)
	}
	for i := range want {
		g := got[i]
		w := want[i]
		if g.Start != w.Start || g.End != w.End || g.Type != w.Type ||
			g.AuthorType != w.AuthorType || g.Tool != w.Tool || !equalStrPtr(g.GenType, w.GenType) {
			t.Errorf("range %d: want %+v, got %+v", i, w, g)
		}
	}
}

// TestBuildNote_SecondCommit_HumanOverridesAI is the core regression test for
// the "AI generates code → commit1 → human edits → commit2 shows human"
// scenario. With the new commit-time attribution model, commit2's sinceNanos
// is set to commit1's timestamp, so the AI edit recorded before commit1 is
// excluded from the lookup. The line therefore falls to the human default.

// TestBuildNote_GenTypeCompletion verifies that lines recorded with
// gen_type=completion (e.g. Cursor Tab or Copilot Tab accepted via the editor
// plugin) are surfaced correctly in by_gen_type.completion, as AI lines in
// totals, and with gen_type="completion" in each per-line entry.

// TestBuildNote_ContentShaSurvivesUserInsert reproduces the line-drift bug: the
// AI Writes a file ("a","b","c"); the user later inserts a line in the middle,
// pushing the AI lines down. The AI lines must stay AI (matched by content_sha
// at their NEW positions) and the user's inserted line must be Human — NOT the
// inverse, which is what positional range matching produced before per-line
// content_sha + the content-only coversLine rule.

// TestBuildNote_CopyPasteOfAIBlockIsHuman reproduces the copy-paste false
// positive: AI generates a 3-line block at lines 1-3 (content_sha recorded at
// those exact lines). In the SAME commit, the user copies that block and pastes
// a duplicate at lines 5-7. The original lines must stay AI; the pasted
// duplicate — same content, different lines — must attribute as Human even
// though its content_sha matches a recorded AI edit.

// TestBuildNote_NarrowedDeltaDuplicateStaysAI reproduces the bug where an AI
// line that legitimately appears TWICE in the final file (the agent generated a
// second copy in a later apply) was mislabeled Human. The first apply is a FULL
// recording (role@3); the second apply is a NARROWED delta that recorded only
// its genuinely-new copy (role@10) and dropped the first copy because it was
// unchanged in the pre-chat snapshot. The committed file has the first copy
// drifted to line 5 and the second at line 10 — both AI. With a max-over-edits
// budget, recorded=1, the exact match at line 10 exhausts it, and line 5 falls
// to Human. Summing narrowed deltas (recorded = max(full) + sum(narrowed) = 2)
// keeps the drifted copy AI.

// TestBuildNote_ClipboardPastePinnedHuman reproduces the worst copy-paste case:
// the human pastes a duplicate of AI content at the very line the AI originally
// produced it (the paste then pushes the AI's own copy down). By content+position
// alone the paste "steals" the exact-position match and reads AI, while the real
// AI line, now drifted, runs out of budget and reads Human — exactly inverted.
// The editor plugin records the paste as a copypaste edit at its exact line; we
// pin that line Human and free the budget so the drifted AI copy reads AI.

// TestBuildNote_AutoformatterDriftNormalizedFallback reproduces the
// "AI generates code, then an autoformatter reflows it" bug: the AI's edit was
// recorded with content_sha for "\treturn 1" (tab indent), but an
// autoformatter rewrote the committed line as "    return 1" (space indent).
// The exact content_sha no longer matches, but content_sha_norm — computed
// from the whitespace-collapsed text — does, so the line must still
// attribute as AI rather than falling through to Human.

// TestBuildNote_NormalizedFallbackCopyPasteIsHuman is the
// content_sha_norm counterpart of TestBuildNote_CopyPasteOfAIBlockIsHuman: the
// AI's "\treturn 1" was reformatted to "    return 1" at line 1 (matches via
// content_sha_norm), and a human separately typed "        return 1" (a
// different indent, same normalized text) at line 3. The reformatted AI line
// must stay AI; the human's same-shape line must not also match via the
// normalized drift fallback.

// TestBuildNote_BlankLineInheritsAIBlock reproduces the "blank lines flip to
// Human after the user edits" bug: the AI wrote a block whose blank lines have
// no (unique) content_sha, so after the file drifts they can't be content-
// matched and default to Human. They must inherit AI from the surrounding block
// rather than be mislabelled human. The user's own non-blank line stays Human.

// TestBuildNote_BlankLineStaysHumanInHumanBlock confirms the inheritance is
// directional: a blank line surrounded by human-typed lines stays Human (it is
// not promoted to AI just because an AI edit exists elsewhere).

// TestBuildNote_MixedCommit_AIAndHumanInSameCommit verifies that a single
// commit where the AI wrote some lines and the human typed others correctly
// splits attribution. AI lines have explicit DB records; human lines have no
// record and fall to the human default (tool="", gen_type=human).

// TestBuildNote_AIDeletedLine_MatchedByContentSHA covers the core
// AI-deletion-attribution case: a Claude CLI edit recorded both an added line
// (line 1, via content_sha) and a removed line's content_sha
// (edit_removed_lines). The committed diff has two deletions: one whose
// content matches the recorded removed-line hash (→ AuthorType "AI",
// Tool "claude", gen_type "cli") and one that doesn't (→ stays "Human", no
// regression).

// TestBuildNote_AIDeletedLine_PureDeletionFile covers the same matching for a
// file with ONLY deletions (no added lines), exercised via the pure-deletion
// FileEntry path rather than flushFile.

// TestBuildNote_AIDeletedBlankLine_InheritsNeighborAttribution covers the
// "kerim" commit bug report: an AI removed a contiguous block of lines that
// included blank lines. tools.RemovedLineHashes never records a content_sha
// for blank lines, so pickEditForRemovedLine can never match the blank
// deleted lines directly — without inheritBlankDeletedLineAttribution they'd
// stay "Human" even though the whole block came from one AI edit.

// TestBuildNote_DeletedBlankLine_HumanNeighborStaysHuman ensures the
// inheritance pass mirrors inheritBlankLineAttribution's "look backward
// first" rule: a blank deleted line whose nearest non-blank neighbour is a
// Human deletion stays "Human", even when an AI-attributed deletion sits
// further away on the other side.

// TestAttributeDeletedLines_MoveCreditsAITool: when an AI tool re-adds a line's
// content elsewhere in the same commit (a move), git scores the old position as
// a deletion — which must be credited to that tool, not Human. Regression for
// the Cursor/Codex/etc. line-move case (e.g. commit fd64d2d2 in mix-test).
func TestAttributeDeletedLines_MoveCreditsAITool(t *testing.T) {
	const content = "    <h1>hello kerim</h1>"
	noEdits := []store.Edit(nil)
	emptyBudget := func() (map[int64]map[string]int, map[int64]map[string]int) {
		return map[int64]map[string]int{}, map[int64]map[string]int{}
	}

	// Move present: deletion of content re-added by cursor → AI/cursor.
	movedAI := map[string]*moveAttr{
		content: {tool: store.ToolCursor, model: "composer-2.5", genType: store.GenTypeChat, remaining: 1},
	}
	totals := &Totals{DeletedLines: 1}
	sha, norm := emptyBudget()
	got := attributeDeletedLines([]DeletedLine{{LineNum: 17, Content: content}}, noEdits, 0, 1<<62, sha, norm, totals, movedAI)
	if len(got) != 1 || got[0].AuthorType != "AI" || got[0].Tool != string(store.ToolCursor) {
		t.Fatalf("move deletion: got %+v, want AI/cursor", got)
	}
	if totals.AIDeletedLines != 1 {
		t.Fatalf("AIDeletedLines = %d, want 1", totals.AIDeletedLines)
	}

	// Consume-once: a second identical deletion with the budget spent stays Human.
	totals2 := &Totals{DeletedLines: 1}
	sha, norm = emptyBudget()
	got2 := attributeDeletedLines([]DeletedLine{{LineNum: 30, Content: content}}, noEdits, 0, 1<<62, sha, norm, totals2, movedAI)
	if got2[0].AuthorType != "Human" {
		t.Fatalf("budget exhausted: got %s, want Human", got2[0].AuthorType)
	}

	// No move, no recorded removal → Human (baseline unchanged).
	totals3 := &Totals{DeletedLines: 1}
	sha, norm = emptyBudget()
	got3 := attributeDeletedLines([]DeletedLine{{LineNum: 5, Content: "<p>unrelated</p>"}}, noEdits, 0, 1<<62, sha, norm, totals3, map[string]*moveAttr{})
	if got3[0].AuthorType != "Human" {
		t.Fatalf("unrelated deletion: got %s, want Human", got3[0].AuthorType)
	}

	// Blank content is never credited as a move even if present in the map.
	totals4 := &Totals{DeletedLines: 1}
	sha, norm = emptyBudget()
	blankMap := map[string]*moveAttr{"   ": {tool: store.ToolCursor, remaining: 1}}
	got4 := attributeDeletedLines([]DeletedLine{{LineNum: 9, Content: "   "}}, noEdits, 0, 1<<62, sha, norm, totals4, blankMap)
	if got4[0].AuthorType != "Human" {
		t.Fatalf("blank move: got %s, want Human", got4[0].AuthorType)
	}
}
