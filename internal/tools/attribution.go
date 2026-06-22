package tools

import (
	"encoding/json"
	"io"
	"path/filepath"

	"github.com/blamely/blamely/internal/authorship"
)

// CaptureBaselineFromStdin handles a PreToolUse hook (`blamely record <tool> --pre`):
// it snapshots the target file's CURRENT content as the pre-edit baseline, so the
// matching PostToolUse `record` diffs the agent's write against the true pre-edit
// state even for a file the editor never had open (Decision B fallback). Flag-gated
// and best-effort; tool-agnostic (reads the file path from the common hook shapes).
// apply_patch payloads (codex/copilot) carry paths in the patch body rather than a
// tool_input.file_path, so those fall back to HEAD at record time — documented gap.
func CaptureBaselineFromStdin(r io.Reader) error {
	if !authorship.Enabled() {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(r, 8<<20))
	if err != nil {
		return nil // best-effort: a pre-hook must never block the tool
	}
	var p struct {
		Cwd       string `json:"cwd"`
		ToolInput struct {
			FilePathSnake string `json:"file_path"`
			FilePathCamel string `json:"filePath"`
			Path          string `json:"path"`
		} `json:"tool_input"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return nil
	}
	fp := firstNonEmpty(p.ToolInput.FilePathSnake, p.ToolInput.FilePathCamel, p.ToolInput.Path)
	if fp == "" {
		return nil // no single target file (e.g. apply_patch) — record falls back to HEAD
	}
	if !filepath.IsAbs(fp) && p.Cwd != "" {
		fp = filepath.Join(p.Cwd, fp)
	}
	_ = authorship.CaptureBaseline(fp)
	return nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// captureAuthorship mirrors a recorded edit into the Attribution working log (the
// pre-edit-baseline + diff engine in internal/authorship). It is:
//   - gated behind authorship.Enabled() (a no-op when disabled);
//   - best-effort and silent — a working-log failure must never affect the host
//     tool (this runs inside a PostToolUse hook);
//   - daemon-independent — it writes the working-log file directly, so call it
//     BEFORE any daemon POST so capture survives a down/unreachable daemon.
//
// author is derived from (tool, genType): an empty tool or a human gen_type
// (typing, copypaste) is Human; anything else is the AI tool.
func captureAuthorship(repoPath, rel, tool, genType, model string) {
	if !authorship.Enabled() || repoPath == "" || rel == "" {
		return
	}
	author := authorship.Author{Type: authorship.AI, Tool: tool, GenType: genType, Model: model}
	if tool == "" || genType == "human" {
		author = authorship.HumanAuthor()
	}
	_, _ = authorship.RecordEdit(filepath.Join(repoPath, rel), author)
}
