package tools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/blamely/blamely/internal/authorship"
)

// captureV2 must be a no-op when the flag is off, and when on must derive the
// right author (human typing vs AI) and chain edits so a human line an AI edit
// re-emits stays Human — the field repro, now exercised through the record-path
// helper end to end.
func TestCaptureV2_FlagGatingAndAuthorChaining(t *testing.T) {
	repo := gitInitRepo(t)
	rel := "f.txt"
	abs := filepath.Join(repo, rel)
	ctx, ok := authorship.ResolveContext(filepath.Join(repo, rel)) // resolves once the file exists
	_ = ctx
	_ = ok

	// Flag OFF → nothing is written.
	t.Setenv("BLAMELY_ATTRIBUTION_V2", "0")
	if err := os.WriteFile(abs, []byte("a\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	captureV2(repo, rel, "claude", "chat", "")
	ctx, ok = authorship.ResolveContext(abs)
	if !ok {
		t.Fatal("ResolveContext failed inside repo")
	}
	if wl, _ := authorship.LoadWorkingLog(ctx.RepoRoot, ctx.Branch, ctx.BaseSHA, ctx.RelPath); wl != nil {
		t.Fatalf("flag off must write no working log, got %+v", wl)
	}

	// Flag ON.
	t.Setenv("BLAMELY_ATTRIBUTION_V2", "1")
	// 1) Human types two lines.
	os.WriteFile(abs, []byte("h1\nh2\n"), 0o644)
	captureV2(repo, rel, "", "human", "")
	// 2) AI re-emits them and appends its own.
	os.WriteFile(abs, []byte("h1\nh2\nai3\n"), 0o644)
	captureV2(repo, rel, "claude", "chat", "claude-opus-4-8")

	wl, err := authorship.LoadWorkingLog(ctx.RepoRoot, ctx.Branch, ctx.BaseSHA, ctx.RelPath)
	if err != nil || wl == nil {
		t.Fatalf("load working log: %v (nil=%v)", err, wl == nil)
	}
	got := map[int]authorship.AuthorType{}
	for _, r := range wl.Lines {
		for ln := r.Start; ln <= r.End; ln++ {
			got[ln] = r.Author.Type
		}
	}
	if got[1] != authorship.Human || got[2] != authorship.Human {
		t.Errorf("human-typed lines must stay Human, got L1=%q L2=%q", got[1], got[2])
	}
	if got[3] != authorship.AI {
		t.Errorf("appended line must be AI, got L3=%q", got[3])
	}
}
