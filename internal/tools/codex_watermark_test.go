package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blamely/blamely/internal/store"
)

// TestTailCodexSession_ResumesFromWatermark verifies Phase-2 persistence for the
// Codex tailer: after a restart (fresh tailer, same DB) it resumes from the saved
// offset — no replay/duplicate of already-flushed edits — and still picks up edits
// appended while it was down.
func TestTailCodexSession_ResumesFromWatermark(t *testing.T) {
	dir := t.TempDir()
	db, err := store.OpenAt(filepath.Join(dir, "wm.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	repo := gitInitRepo(t)
	target := filepath.Join(repo, "a.go")
	if err := os.WriteFile(target, []byte("line one\nline two\nline three\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := filepath.Join(dir, "rollout-1.jsonl")
	// turn_context (model) + patch_apply_end (add) + token_count (flush → emit).
	turn := mustMarshalWrapped(t, "turn_context", map[string]any{"model": "gpt-5.5"})
	patch := mustMarshalWrapped(t, "event_msg", map[string]any{
		"type": "patch_apply_end", "success": true,
		"changes": map[string]any{target: map[string]any{"type": "add", "content": "line one\nline two\nline three\n"}},
	})
	tok := mustMarshalWrapped(t, "event_msg", map[string]any{
		"type": "token_count",
		"info": map[string]any{"last_token_usage": map[string]any{"input_tokens": 10, "output_tokens": 5}},
	})
	writeLines := func(lines ...[]byte) {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range lines {
			f.Write(l)
			f.Write([]byte("\n"))
		}
		f.Close()
	}
	writeLines(turn, patch, tok)

	// run drives the tailer until either a watermark is observed (wantWatermark)
	// or a short settle elapses, then cancels and returns how many events emitted.
	run := func(wantWatermarkAtLeast int64) int {
		sink := &mockSink{}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { _ = tailCodexSession(ctx, p, sink, db, "codex"); close(done) }()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if wm, ok := db.LoadWatermark("codex", p); ok && wm.ByteOffset >= wantWatermarkAtLeast {
				break
			}
			time.Sleep(15 * time.Millisecond)
		}
		time.Sleep(120 * time.Millisecond) // let any (unexpected) extra events land
		cancel()
		<-done
		return len(sink.events)
	}

	if n := run(1); n != 1 {
		t.Fatalf("initial run: emitted %d events, want 1", n)
	}
	wm1, _ := db.LoadWatermark("codex", p)
	if wm1.ByteOffset == 0 {
		t.Fatal("watermark not advanced after initial run")
	}

	// Restart with no new data → resume from watermark, emit nothing, no replay.
	if n := run(wm1.ByteOffset); n != 0 {
		t.Fatalf("resume with no new data: emitted %d events, want 0 (replayed)", n)
	}

	// Append a second add+flush; restart must pick up only the new edit.
	target2 := filepath.Join(repo, "b.go")
	if err := os.WriteFile(target2, []byte("xx\nyy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch2 := mustMarshalWrapped(t, "event_msg", map[string]any{
		"type": "patch_apply_end", "success": true,
		"changes": map[string]any{target2: map[string]any{"type": "add", "content": "xx\nyy\n"}},
	})
	writeLines(patch2, tok)

	if n := run(wm1.ByteOffset + 1); n != 1 {
		t.Fatalf("after append: emitted %d events, want 1 (only the new edit)", n)
	}
}
