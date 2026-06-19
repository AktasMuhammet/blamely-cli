package store

import (
	"testing"
	"time"
)

func TestWatermark_LoadMissingReturnsNotOk(t *testing.T) {
	db := openTestDB(t)
	if _, ok := db.LoadWatermark("copilot-chat", "/x/y.jsonl"); ok {
		t.Fatal("expected ok=false for an unseen source")
	}
}

func TestWatermark_SaveLoadRoundTrip(t *testing.T) {
	db := openTestDB(t)
	in := Watermark{ByteOffset: 4096, Size: 8192, MtimeNanos: 123456789, Extra: `{"tegOffset":2048,"nextReqIdx":3}`}
	if err := db.SaveWatermark("copilot-chat", "/x/y.jsonl", in); err != nil {
		t.Fatal(err)
	}
	got, ok := db.LoadWatermark("copilot-chat", "/x/y.jsonl")
	if !ok {
		t.Fatal("expected ok=true after save")
	}
	if got != in {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, in)
	}
}

func TestWatermark_UpsertOverwrites(t *testing.T) {
	db := openTestDB(t)
	_ = db.SaveWatermark("codex", "/s/events.jsonl", Watermark{ByteOffset: 100, Size: 100})
	_ = db.SaveWatermark("codex", "/s/events.jsonl", Watermark{ByteOffset: 250, Size: 250})
	got, _ := db.LoadWatermark("codex", "/s/events.jsonl")
	if got.ByteOffset != 250 || got.Size != 250 {
		t.Fatalf("upsert did not overwrite: %+v", got)
	}
}

func TestWatermark_KeyedByWatcherAndSource(t *testing.T) {
	db := openTestDB(t)
	_ = db.SaveWatermark("copilot-chat", "/a.jsonl", Watermark{ByteOffset: 1})
	_ = db.SaveWatermark("cursor-chat", "/a.jsonl", Watermark{ByteOffset: 2}) // same source, different watcher
	_ = db.SaveWatermark("copilot-chat", "/b.jsonl", Watermark{ByteOffset: 3})
	a1, _ := db.LoadWatermark("copilot-chat", "/a.jsonl")
	a2, _ := db.LoadWatermark("cursor-chat", "/a.jsonl")
	b1, _ := db.LoadWatermark("copilot-chat", "/b.jsonl")
	if a1.ByteOffset != 1 || a2.ByteOffset != 2 || b1.ByteOffset != 3 {
		t.Fatalf("rows collided: a1=%d a2=%d b1=%d", a1.ByteOffset, a2.ByteOffset, b1.ByteOffset)
	}
}

func TestWatermark_DeleteAndPrune(t *testing.T) {
	db := openTestDB(t)
	_ = db.SaveWatermark("copilot-chat", "/keep.jsonl", Watermark{ByteOffset: 1})
	_ = db.SaveWatermark("copilot-chat", "/gone.jsonl", Watermark{ByteOffset: 1})

	if err := db.DeleteWatermark("copilot-chat", "/gone.jsonl"); err != nil {
		t.Fatal(err)
	}
	if _, ok := db.LoadWatermark("copilot-chat", "/gone.jsonl"); ok {
		t.Fatal("deleted watermark should be gone")
	}

	// Prune everything updated before "now+1s" → removes the survivor too.
	n, err := db.PruneWatermarks(time.Now().Add(time.Second).UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("prune removed %d, want 1", n)
	}
	if _, ok := db.LoadWatermark("copilot-chat", "/keep.jsonl"); ok {
		t.Fatal("pruned watermark should be gone")
	}
}
