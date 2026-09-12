package tools

import (
	"encoding/json"
	"io"
	"path/filepath"

	"github.com/blamely/blamely/internal/authorship"
	"github.com/blamely/blamely/internal/gitutil"
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
	raw, err := readHookPayload(r)
	if err != nil {
		return nil // best-effort: a pre-hook must never block the tool
	}
	var p struct {
		Cwd       string `json:"cwd"`
		ToolName  string `json:"tool_name"`
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
		// A shell command names no target file — it may write anything — so snapshot
		// the repo's untracked-by-Attribution files instead. apply_patch still falls
		// back to HEAD at record time (paths live in the patch body): documented gap.
		if isShellToolName(p.ToolName) {
			captureShellBaselines(p.Cwd)
		}
		return nil
	}
	if !filepath.IsAbs(fp) && p.Cwd != "" {
		fp = filepath.Join(p.Cwd, fp)
	}
	captureBaselineIfUntracked(fp)
	return nil
}

// captureBaselineIfUntracked snapshots absPath's current content as its pre-edit
// baseline, but only while Attribution isn't tracking the file yet — which is
// exactly the "a file the editor never had open" case the pre-hook exists for.
//
// The guard matters: for a file that DOES have a working log, the stored baseline
// is what its current line attributions describe. Overwriting it with today's
// content strands those line numbers, and a subsequent commit can then read the
// user's own lines off a shifted mapping and mark them AI. That failure is why
// PreToolUse was switched off for Claude (see install.claudeHookEvents); the guard
// removes the hazard rather than the hook.
func captureBaselineIfUntracked(absPath string) {
	ctx, ok := authorship.ResolveContext(absPath)
	if !ok {
		return
	}
	if wl, err := authorship.LoadWorkingLog(ctx.RepoRoot, ctx.Branch, ctx.BaseSHA, ctx.RelPath); err == nil && wl != nil {
		return
	}
	_ = authorship.CaptureBaseline(absPath)
}

// maxShellBaselineFiles caps how many pre-command baselines one shell command
// snapshots. Past that the working tree is mid-rebase / mid-checkout rather than
// mid-authoring, and the reads would cost more than the attribution is worth.
const maxShellBaselineFiles = 60

// captureShellBaselines snapshots the pre-command content of every changed source
// file that Attribution is NOT yet tracking, for each repo under cwd.
//
// Without it, the FIRST observation of a file diffs against HEAD — so a shell
// write claims every uncommitted change the file had accumulated, including lines
// the user typed by hand, and the user's own work commits as AI. That is the exact
// inversion of the product's rule (an unobserved line is Human), so the pre-edit
// snapshot is what keeps a shell write's claim honest.
//
// A file that already HAS a working log is skipped: its stored baseline is what
// its current attributions describe, and overwriting that would strand their line
// numbers. Committed files are covered by the post-commit seed, which writes both
// a log and a baseline — so in practice this fills the gap for files no commit has
// noted yet (new files, and files whose uncommitted edits nobody observed).
func captureShellBaselines(cwd string) {
	if !authorship.Enabled() || cwd == "" {
		return
	}
	for _, root := range gitutil.DiscoverRepos(cwd) {
		files := changedSourceFiles(root)
		if len(files) == 0 || len(files) > maxShellBaselineFiles {
			continue
		}
		for _, rel := range files {
			captureBaselineIfUntracked(filepath.Join(root, rel))
		}
	}
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
