package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blamely/blamely/internal/authorship"
)

// captureAuthorship must derive the right author (human typing vs AI) and chain edits so a
// human line an AI edit re-emits stays Human — the field repro, exercised through the
// record-path helper end to end. (attribution is always on; there is no flag to gate.)
func TestCaptureAuthorship_AuthorChaining(t *testing.T) {
	repo := gitInitRepo(t)
	rel := "f.txt"
	abs := filepath.Join(repo, rel)

	// 1) Human types two lines.
	os.WriteFile(abs, []byte("h1\nh2\n"), 0o644)
	captureAuthorship(repo, rel, "", "human", "")
	ctx, ok := authorship.ResolveContext(abs) // resolves once the file exists
	if !ok {
		t.Fatal("ResolveContext failed inside repo")
	}
	// 2) AI re-emits them and appends its own.
	os.WriteFile(abs, []byte("h1\nh2\nai3\n"), 0o644)
	captureAuthorship(repo, rel, "claude", "chat", "claude-opus-4-8")

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

// record --pre (CaptureBaselineFromStdin) snapshots the pre-edit content so the
// matching post-edit captureAuthorship diffs against it — the terminal-agent baseline
// fallback. Here: human content exists, --pre captures it, then an AI edit appends;
// the human lines must stay Human.
func TestCaptureBaselineFromStdin_FeedsPostEditDiff(t *testing.T) {
	repo := gitInitRepo(t)
	rel := "f.txt"
	abs := filepath.Join(repo, rel)
	os.WriteFile(abs, []byte("h1\nh2\n"), 0o644)

	// PreToolUse: capture the current (human) content as the baseline.
	payload := `{"cwd":` + jsonStr(repo) + `,"tool_input":{"file_path":` + jsonStr(abs) + `}}`
	if err := CaptureBaselineFromStdin(strings.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	// The agent then appends a line; PostToolUse records it.
	os.WriteFile(abs, []byte("h1\nh2\nai3\n"), 0o644)
	captureAuthorship(repo, rel, "claude", "chat", "")

	ctx, ok := authorship.ResolveContext(abs)
	if !ok {
		t.Fatal("resolve ctx")
	}
	wl, err := authorship.LoadWorkingLog(ctx.RepoRoot, ctx.Branch, ctx.BaseSHA, ctx.RelPath)
	if err != nil || wl == nil {
		t.Fatalf("load working log: %v", err)
	}
	got := map[int]authorship.AuthorType{}
	for _, r := range wl.Lines {
		for ln := r.Start; ln <= r.End; ln++ {
			got[ln] = r.Author.Type
		}
	}
	if got[1] != authorship.Human || got[2] != authorship.Human || got[3] != authorship.AI {
		t.Errorf("want H,H,AI; got L1=%q L2=%q L3=%q", got[1], got[2], got[3])
	}
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
