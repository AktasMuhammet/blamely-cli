package store

import (
	"testing"
	"time"
)

// TestUpgradeRecentCompletionsToChat verifies that a chat marker retroactively
// flips a recent medium-confidence copilot completion edit to chat, while
// leaving confirmed (high-confidence) inline accepts and other tools alone.
func TestUpgradeRecentCompletionsToChat(t *testing.T) {
	db := openTestDB(t)
	now := time.Now().UnixNano()

	mk := func(tsOffset time.Duration, tool Tool, gt GenType, conf Confidence, file string) int64 {
		id, err := db.InsertEdit(Edit{
			TimestampNanos: now + int64(tsOffset),
			RepoPath:       "/repo", FilePath: file, Tool: tool,
			Confidence: conf, GenType: gt,
			Lines: []EditLine{{StartLine: 1, EndLine: 2}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	applyID := mk(-30*time.Second, ToolCopilot, GenTypeCompletion, ConfidenceMedium, "a.go") // should upgrade
	tabID := mk(-20*time.Second, ToolCopilot, GenTypeCompletion, ConfidenceHigh, "b.go")     // confirmed tab → keep
	cursorID := mk(-10*time.Second, ToolCursor, GenTypeCompletion, ConfidenceMedium, "c.go") // other tool → keep
	oldID := mk(-10*time.Minute, ToolCopilot, GenTypeCompletion, ConfidenceMedium, "d.go")   // outside window → keep

	if err := db.UpgradeRecentCompletionsToChat(ToolCopilot, now, int64(60*time.Second)); err != nil {
		t.Fatal(err)
	}

	want := map[int64]GenType{
		applyID:  GenTypeChat,
		tabID:    GenTypeCompletion,
		cursorID: GenTypeCompletion,
		oldID:    GenTypeCompletion,
	}
	for id, exp := range want {
		var gt string
		if err := db.QueryRow("SELECT gen_type FROM edits WHERE id=?", id).Scan(&gt); err != nil {
			t.Fatal(err)
		}
		if GenType(gt) != exp {
			t.Errorf("edit %d: gen_type=%q, want %q", id, gt, exp)
		}
	}
}
