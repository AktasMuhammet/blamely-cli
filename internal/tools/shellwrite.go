package tools

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/blamely/blamely/internal/daemon"
	"github.com/blamely/blamely/internal/gitutil"
)

// shellwrite.go is the ONE shell-write attribution path, shared by every agent
// that can write a file by running a command instead of calling its edit tool
// (Claude `Bash`, Codex `shell_command`, Copilot `run_in_terminal`, Gemini
// `run_shell_command`). Keeping it single-sourced is deliberate: the per-tool
// copies drifted before, and the copy that forgot to mirror into the Attribution
// working log silently committed the agent's work as Human.
//
// We cannot parse arbitrary shell (`> "$fname"`, a script, a formatter), so the
// resolution is: ask git which source files in the repo just changed, and credit
// those to the command that just ran.

// shellWriteOpts is the per-tool half of a shell-write record.
type shellWriteOpts struct {
	Tool           string
	GenType        string
	Model          string
	SessionID      string
	TranscriptPath string
	// WriteSource and DeleteSource tag the RawMeta of write/delete rows so the
	// origin of a row stays greppable per tool (e.g. "claude_bash_fswrite").
	WriteSource  string
	DeleteSource string
	// Window overrides the mtime window for writes. Zero means bashWriteWindow.
	// Claude passes a transcript-derived window that covers the command's whole
	// run; the other tools have no comparable timestamp and take the floor.
	Window time.Duration
}

// recordShellWrites attributes the files that changed while a shell command ran,
// for every repo under cwd. A cwd ABOVE the repos (a workspace dir holding
// separate `backend/` and `frontend/` clones) resolves to every repo nested
// beneath it, so an agent started there still gets its shell writes attributed —
// see DiscoverRepos.
func recordShellWrites(cwd, command string, o shellWriteOpts) error {
	return recordShellWritesInRoots(gitutil.DiscoverRepos(cwd), command, o)
}

// recordShellWritesInRoots is recordShellWrites over an already-resolved set of
// repo roots, for callers that need to know whether any repo was found at all.
func recordShellWritesInRoots(roots []string, command string, o shellWriteOpts) error {
	for _, root := range roots {
		if err := recordShellWritesIn(root, command, o); err != nil {
			return err
		}
	}
	return nil
}

// recordShellWritesIn is recordShellWrites scoped to one repo root.
func recordShellWritesIn(root, command string, o shellWriteOpts) error {
	// Deletions the command performed (e.g. `rm foo.html`), scoped to the
	// command's actual rm/del targets so a file the USER deleted by hand isn't
	// swept in. Done first (and unconditionally) so a command that ONLY deletes —
	// leaving recentlyChangedFiles empty — still records its removals.
	if err := recordShellDeletions(root, command, o.Tool, o.GenType, o.Model, o.SessionID, o.TranscriptPath, o.DeleteSource); err != nil {
		return err
	}
	for _, payload := range shellWritePayloads(root, o) {
		// Mirror into the Attribution working log BEFORE the daemon POST, exactly
		// like every edit-tool path does. The commit-time flip reads the working
		// log, so a shell-written file stays invisible to it however many DB rows
		// it has. root (not payload.RepoPath) because captureAuthorship joins a
		// filesystem path, while RepoPath carries the canonical repo ID.
		captureAuthorship(root, payload.FilePath, o.Tool, o.GenType, payload.Model)
		if err := postToDaemon(payload); err != nil {
			return err
		}
	}
	return nil
}

// shellWritePayloads builds the edit payloads for the files a shell command just
// wrote inside `root`. Split out from the recording so the resolution can be
// asserted in tests without a running daemon. Each file is recorded as a
// medium-confidence whole-file edit, matching how a Write is stored.
func shellWritePayloads(root string, o shellWriteOpts) []daemon.EditPayload {
	window := o.Window
	if window <= 0 {
		window = bashWriteWindow
	}
	files := recentlyChangedFiles(root, window)
	// none → read-only command (ls/grep/test); too many → bulk op we won't guess.
	if len(files) == 0 || len(files) > maxBashWriteFiles {
		return nil
	}
	repoID, _ := gitutil.RepoID(resolveSymlinks(root))
	if repoID == "" {
		repoID = root
	}
	out := make([]daemon.EditPayload, 0, len(files))
	for _, rel := range files {
		abs := filepath.Join(root, rel)
		ranges := perLineShaRanges(abs)
		if len(ranges) == 0 {
			continue
		}
		payload := daemon.EditPayload{
			Tool:           o.Tool,
			Confidence:     "medium",
			GenType:        o.GenType,
			Model:          o.Model,
			RepoPath:       repoID,
			FilePath:       rel,
			SuggestedLines: int64(len(ranges)),
			Lines:          toDaemonRanges(ranges),
			RawMeta: fmt.Sprintf(`{"session_id":%q,"tool":%q,"source":%q,"transcript_path":%q}`,
				o.SessionID, o.Tool, o.WriteSource, o.TranscriptPath),
		}
		// Backfill model + token usage from the session transcript, exactly like
		// the edit-tool hook path — otherwise the row has an empty model.
		applyHookUsage(&payload, hookUsageOptions{
			transcriptPath: o.TranscriptPath,
			sessionID:      o.SessionID,
			tool:           o.Tool,
		})
		out = append(out, payload)
	}
	return out
}

// shellToolNames are the tool names that run a shell command, across every agent
// we hook. Used to recognise a shell payload when it carries no single target
// file path — the shape both the pre-edit baseline capture and the write
// attribution key off.
var shellToolNames = map[string]bool{
	// Claude Code
	"bash": true,
	// Copilot
	"run_in_terminal": true,
	// Gemini
	"run_shell_command": true,
	// Cursor
	"shell": true,
	// Codex (codexShellNameList; "shell" already above)
	"shell_command":    true,
	"exec_command":     true,
	"local_shell_call": true,
	"local_shell":      true,
	"container.exec":   true,
}

// isShellToolName reports whether name is one of the shell-running tools.
func isShellToolName(name string) bool { return shellToolNames[strings.ToLower(name)] }
