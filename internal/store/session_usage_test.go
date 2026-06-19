package store

import (
	"testing"
	"time"
)

func TestSessionUsage_SaveLoadRoundTrip(t *testing.T) {
	db := openTestDB(t)
	in := SessionUsage{InputTokens: 26512, OutputTokens: 929, CacheReadTokens: 15872, CacheWriteTokens: 0, ReasoningTokens: 704}
	if err := db.SaveSessionUsage("sess-1", "copilot", "gpt-5-mini", in); err != nil {
		t.Fatal(err)
	}
	got, ok := db.LoadSessionUsage("sess-1", "copilot", "gpt-5-mini")
	if !ok {
		t.Fatal("expected ok=true after save")
	}
	if got != in {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, in)
	}
}

func TestSessionUsage_UpsertOverwrites(t *testing.T) {
	db := openTestDB(t)
	_ = db.SaveSessionUsage("s", "copilot", "gpt-5.2", SessionUsage{InputTokens: 100, OutputTokens: 10})
	_ = db.SaveSessionUsage("s", "copilot", "gpt-5.2", SessionUsage{InputTokens: 250, OutputTokens: 40})
	got, _ := db.LoadSessionUsage("s", "copilot", "gpt-5.2")
	if got.InputTokens != 250 || got.OutputTokens != 40 {
		t.Fatalf("upsert did not overwrite (cumulative totals must replace): %+v", got)
	}
}

func TestSessionUsage_KeyedBySessionToolModel(t *testing.T) {
	db := openTestDB(t)
	_ = db.SaveSessionUsage("s", "copilot", "gpt-5-mini", SessionUsage{InputTokens: 1})
	_ = db.SaveSessionUsage("s", "copilot", "gpt-5.2", SessionUsage{InputTokens: 2}) // same session, different model
	a, _ := db.LoadSessionUsage("s", "copilot", "gpt-5-mini")
	b, _ := db.LoadSessionUsage("s", "copilot", "gpt-5.2")
	if a.InputTokens != 1 || b.InputTokens != 2 {
		t.Fatalf("rows collided: a=%d b=%d", a.InputTokens, b.InputTokens)
	}
	if _, ok := db.LoadSessionUsage("s", "copilot", "nope"); ok {
		t.Fatal("expected ok=false for an unrecorded model")
	}
}

func TestRecentSessionUsage_OrderAndLimit(t *testing.T) {
	db := openTestDB(t)
	// Inserted oldest→newest; updated_nanos is set at save time, so newest is last.
	_ = db.SaveSessionUsage("old", "copilot", "gpt-5-mini", SessionUsage{InputTokens: 1})
	time.Sleep(2 * time.Millisecond)
	_ = db.SaveSessionUsage("new", "copilot", "gpt-5.2", SessionUsage{InputTokens: 2})

	rows, err := db.RecentSessionUsage(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].SessionID != "new" {
		t.Fatalf("expected newest-first; got %+v", rows)
	}
	if got, _ := db.RecentSessionUsage(1); len(got) != 1 || got[0].SessionID != "new" {
		t.Fatalf("limit not applied: %+v", got)
	}
}
