package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestCodexOverwriteAddRecordsRemovedLines is the regression for "Codex CLI
// deletions show as Human". When Codex replaces an EXISTING file's whole
// contents ("replace the entire file"), it reports the change as an "add" (its
// own summary prints "+N -0"), not an "update". The old HEAD lines it overwrote
// are deletions BY codex, but the "add" path never fingerprinted them, so at
// commit they fell back to Human. This asserts the overwrite now emits removed
// lines for the old content.
func TestCodexOverwriteAddRecordsRemovedLines(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) {
		c := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Skipf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "t@t.t")
	git("config", "user.name", "t")

	// Old committed content (the 3 lines Codex will overwrite).
	rel := "employ_register.html"
	target := filepath.Join(repo, rel)
	old := "<html>\n<body>old form</body>\n</html>\n"
	if err := os.WriteFile(target, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-qm", "old form")

	// Codex overwrites the whole file with new content, reported as an "add".
	newContent := "<!DOCTYPE html>\n<html lang=\"tr\">\n<head><title>Yeni</title></head>\n<body>new</body>\n</html>\n"
	if err := os.WriteFile(target, []byte(newContent), 0o644); err != nil {
		t.Fatal(err)
	}

	sink := &mockSink{}
	st := &codexState{sink: sink, model: "gpt-5.5"}

	patchEnd := mustMarshalWrapped(t, "event_msg", map[string]any{
		"type":    "patch_apply_end",
		"success": true,
		"changes": map[string]any{
			target: map[string]any{"type": "add", "content": newContent},
		},
	})
	processCodexLine(patchEnd, st)
	st.flush(0, 0, 0, 0, false)

	if len(sink.events) != 1 {
		t.Fatalf("expected 1 recorded event, got %d", len(sink.events))
	}
	ev := sink.events[0]
	if ev.Tool != "codex" || ev.GenType != "cli" {
		t.Errorf("tool=%q gen_type=%q, want codex/cli", ev.Tool, ev.GenType)
	}
	if ev.FilePath != rel {
		t.Errorf("file = %q, want %q", ev.FilePath, rel)
	}
	// The new lines are still recorded as added.
	if len(ev.Lines) == 0 {
		t.Errorf("expected added line ranges for the new content, got none")
	}
	// The overwritten HEAD lines must be fingerprinted as removed, so the commit
	// credits codex for the deletion instead of Human.
	if len(ev.RemovedLines) == 0 {
		t.Fatalf("expected removed-line hashes for the overwritten old content, got none " +
			"(deletions would fall back to Human)")
	}
}

// TestCodexAddBrandNewFileHasNoRemovedLines guards the fix: a genuinely new file
// (not present at HEAD) must NOT report any removed lines — otherwise the
// overwrite fix would invent phantom deletions for every created file.
func TestCodexAddBrandNewFileHasNoRemovedLines(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) {
		c := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Skipf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "t@t.t")
	git("config", "user.name", "t")

	rel := "brand_new.html"
	target := filepath.Join(repo, rel)
	content := "<!DOCTYPE html>\n<html></html>\n"
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	sink := &mockSink{}
	st := &codexState{sink: sink, model: "gpt-5.5"}
	patchEnd := mustMarshalWrapped(t, "event_msg", map[string]any{
		"type":    "patch_apply_end",
		"success": true,
		"changes": map[string]any{
			target: map[string]any{"type": "add", "content": content},
		},
	})
	processCodexLine(patchEnd, st)
	st.flush(0, 0, 0, 0, false)

	if len(sink.events) != 1 {
		t.Fatalf("expected 1 recorded event, got %d", len(sink.events))
	}
	if got := len(sink.events[0].RemovedLines); got != 0 {
		t.Errorf("brand-new file must have 0 removed lines, got %d", got)
	}
}
