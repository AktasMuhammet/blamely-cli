package tools

import (
	"path/filepath"

	"github.com/blamely/blamely/internal/authorship"
)

// captureV2 mirrors a recorded edit into the Attribution v2 working log (the
// pre-edit-baseline + diff engine in internal/authorship). It is:
//   - gated behind authorship.Enabled() — a no-op when the flag is off, so v2 runs
//     in DUAL with the existing recorder (Phase 1–2) and the note/gutter are
//     unaffected until the Phase 3 flip;
//   - best-effort and silent — a working-log failure must never affect the host
//     tool (this runs inside a PostToolUse hook);
//   - daemon-independent — it writes the working-log file directly, so call it
//     BEFORE any daemon POST so capture survives a down/unreachable daemon.
//
// author is derived from (tool, genType): an empty tool or a human gen_type
// (typing, copypaste) is Human; anything else is the AI tool.
func captureV2(repoPath, rel, tool, genType, model string) {
	if !authorship.Enabled() || repoPath == "" || rel == "" {
		return
	}
	author := authorship.Author{Type: authorship.AI, Tool: tool, GenType: genType, Model: model}
	if tool == "" || genType == "human" {
		author = authorship.HumanAuthor()
	}
	_, _ = authorship.RecordEdit(filepath.Join(repoPath, rel), author)
}
