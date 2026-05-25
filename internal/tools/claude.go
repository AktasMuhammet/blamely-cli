package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/blamely/blamely/internal/daemon"
	"github.com/blamely/blamely/internal/gitutil"
)

// Claude Code's PostToolUse hook pipes a payload like:
// {
//   "session_id": "...",
//   "transcript_path": "/Users/.../.claude/projects/.../<uuid>.jsonl",
//   "cwd": "/path/to/repo",
//   "tool_name": "Edit",   // or Write, MultiEdit, NotebookEdit
//   "tool_input": { ... }
// }
//
// We extract file_path, locate the new content's line range, then enrich
// with model+tokens from the transcript before POSTing to the daemon.

// claudeHookPayload is the JSON body that both Claude Code AND Cursor send
// to a PostToolUse hook. Cursor includes `cursor_version`; Claude Code does not.
type claudeHookPayload struct {
	SessionID      string          `json:"session_id"`
	ConversationID string          `json:"conversation_id"` // Cursor alias for session_id
	TranscriptPath string          `json:"transcript_path"`
	Cwd            string          `json:"cwd"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	// Cursor-specific fields
	CursorVersion string `json:"cursor_version"`
	Model         string `json:"model"` // Cursor puts the model in the top-level payload
	// GenType may be set by Cursor to "completion" for Tab accepts vs "chat"
	// for Composer edits. When empty we infer from the session context.
	GenType string `json:"gen_type"`
}

type editInput struct {
	FilePath  string `json:"file_path"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

type writeInput struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

type multiEditInput struct {
	FilePath string `json:"file_path"`
	Edits    []struct {
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
	} `json:"edits"`
}

// RecordClaudeFromStdin handles the PostToolUse hook payload sent by both
// Claude Code AND Cursor — they share the same hooks framework. The payload
// is distinguished by the presence of `cursor_version`: Cursor payloads
// include it, Claude Code payloads do not.
func RecordClaudeFromStdin(r io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(r, 8<<20))
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	var p claudeHookPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("parse hook payload: %w", err)
	}
	// Cursor sends session_id under conversation_id in some versions.
	if p.SessionID == "" && p.ConversationID != "" {
		p.SessionID = p.ConversationID
	}

	isCursor := p.CursorVersion != ""

	filePath, ranges, suggested, err := extractClaudeRanges(p)
	if err != nil {
		return err
	}
	if filePath == "" {
		return nil
	}

	resolvedFile := resolveSymlinks(filePath)
	repoPath, _ := gitutil.RepoID(resolvedFile)
	if repoPath == "" && p.Cwd != "" {
		repoPath, _ = gitutil.RepoID(resolveSymlinks(p.Cwd))
	}
	wt, _ := gitutil.Toplevel(resolvedFile)
	rel := resolvedFile
	if wt != "" {
		if r, err := filepath.Rel(wt, resolvedFile); err == nil && !strings.HasPrefix(r, "..") {
			rel = r
		}
	}

	tool := "claude"
	if isCursor {
		tool = "cursor"
	}

	// Determine generation type.
	//
	// Claude Code is always a chat session.
	//
	// For Cursor we distinguish two sources:
	//   - Composer (chat): fires from within a conversation → session_id or
	//     conversation_id is set in the payload.
	//   - Cursor Tab (inline completion): no conversation context → both IDs
	//     are empty. When Cursor sets gen_type explicitly in the payload we
	//     honour that directly; otherwise we use the session-presence heuristic.
	genType := "chat"
	if isCursor {
		switch {
		case p.GenType != "":
			// Cursor explicitly told us what this is.
			genType = p.GenType
		case p.SessionID == "" && p.ConversationID == "":
			// No conversation context → inline Tab completion.
			genType = "completion"
		}
	}

	payload := daemon.EditPayload{
		Tool:           tool,
		Confidence:     "high",
		GenType:        genType,
		RepoPath:       repoPath,
		FilePath:       rel,
		SuggestedLines: suggested,
		Lines:          toDaemonRanges(ranges),
		RawMeta: fmt.Sprintf(`{"session_id":%q,"tool":%q,"cursor_version":%q,"transcript_path":%q}`,
			p.SessionID, p.ToolName, p.CursorVersion, p.TranscriptPath),
	}

	if isCursor {
		// Cursor puts the model name directly in the hook payload.
		// Token usage is not exposed by Cursor's hook payload.
		if p.Model != "" {
			payload.Model = p.Model
		}
	} else {
		// Claude Code: read model + tokens from the transcript JSONL.
		usage, _ := ReadTranscriptUsage(p.TranscriptPath)
		if usage != nil {
			payload.Model = usage.Model
			payload.InputTokens = int64Ptr(usage.InputTokens)
			payload.OutputTokens = int64Ptr(usage.OutputTokens)
			payload.CacheReadTokens = int64Ptr(usage.CacheReadTokens)
			payload.CacheWriteTokens = int64Ptr(usage.CacheWriteTokens)
		}
	}

	return postToDaemon(payload)
}

// extractClaudeRanges returns the file path, the located line ranges after
// the edit landed, and `suggested`: the number of lines the model proposed
// before any user editing. For Edit/MultiEdit `suggested` is the new_string
// line count summed across edits; for Write it's the full content's line
// count. The watcher records this on the edit row so a later attribution
// pass can show "claude suggested 10, accepted 6" when the user overrode
// some of the AI-produced lines.
func extractClaudeRanges(p claudeHookPayload) (string, []LineRange, int64, error) {
	switch p.ToolName {
	case "Edit":
		var in editInput
		if err := json.Unmarshal(p.ToolInput, &in); err != nil {
			return "", nil, 0, fmt.Errorf("parse Edit input: %w", err)
		}
		if in.FilePath == "" {
			return "", nil, 0, nil
		}
		// Deletion case: new_string is empty (or whitespace-only) and
		// old_string was non-empty. We can't locate the deleted text in the
		// post-edit file (it's gone), but we still credit the AI with the
		// removal in `suggested_lines` so the report shows the AI touched
		// these many lines. Pre-image line numbers are filled in at commit
		// time by the diff parser; we record the file path here so the
		// human-edit watcher's `recent AI activity` lookup suppresses any
		// competing human-edit row for the same file.
		if strings.TrimSpace(in.NewString) == "" && in.OldString != "" {
			return in.FilePath, nil, int64(countLines(in.OldString)), nil
		}
		// Credit the AI only for the lines that are genuinely new — not for
		// context lines that happen to be inside new_string for the match to
		// be precise. We always compute the line-count diff so suggested
		// stays accurate even when we can't locate the text in the file.
		fullRange, err := LocateNewString(in.FilePath, in.NewString)
		if err != nil {
			return in.FilePath, nil, CountAddedLines(in.OldString, in.NewString), err
		}
		if fullRange == nil {
			return in.FilePath, nil, CountAddedLines(in.OldString, in.NewString), nil
		}
		ranges, suggested := narrowToChangedLines(in.OldString, in.NewString, *fullRange)
		return in.FilePath, ranges, suggested, nil

	case "Write":
		var in writeInput
		if err := json.Unmarshal(p.ToolInput, &in); err != nil {
			return "", nil, 0, fmt.Errorf("parse Write input: %w", err)
		}
		if in.FilePath == "" {
			return "", nil, 0, nil
		}
		suggested := int64(countLines(in.Content))
		lr, err := LineRangeForWholeFile(in.FilePath)
		if err != nil {
			return in.FilePath, nil, suggested, err
		}
		if lr == nil {
			return in.FilePath, nil, suggested, nil
		}
		return in.FilePath, []LineRange{*lr}, suggested, nil

	case "MultiEdit":
		var in multiEditInput
		if err := json.Unmarshal(p.ToolInput, &in); err != nil {
			return "", nil, 0, fmt.Errorf("parse MultiEdit input: %w", err)
		}
		if in.FilePath == "" {
			return "", nil, 0, nil
		}
		var suggested int64
		var out []LineRange
		for _, ed := range in.Edits {
			// Sub-edit deletion: empty NewString means this sub-edit removed
			// old_string. Credit the AI for the number of lines deleted via
			// suggested_lines, but don't try to locate the (now-missing) text.
			if strings.TrimSpace(ed.NewString) == "" && ed.OldString != "" {
				suggested += int64(countLines(ed.OldString))
				continue
			}
			lr, err := LocateNewString(in.FilePath, ed.NewString)
			if err != nil || lr == nil {
				// Can't locate, but still credit the AI's net-new line count.
				suggested += CountAddedLines(ed.OldString, ed.NewString)
				continue
			}
			narrowed, narrowSuggest := narrowToChangedLines(ed.OldString, ed.NewString, *lr)
			out = append(out, narrowed...)
			suggested += narrowSuggest
		}
		return in.FilePath, out, suggested, nil

	case "NotebookEdit":
		var in struct {
			NotebookPath string `json:"notebook_path"`
		}
		if err := json.Unmarshal(p.ToolInput, &in); err != nil {
			return "", nil, 0, fmt.Errorf("parse NotebookEdit input: %w", err)
		}
		// We don't attempt line-range attribution inside notebook cells in v1.
		// Recording a marker so we can attribute at least "the notebook was AI-edited".
		return in.NotebookPath, nil, 0, nil

	default:
		return "", nil, 0, nil
	}
}

// countLines returns the number of lines in s. Empty string → 0. A non-empty
// string with no trailing newline still counts its content as one line.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

func toDaemonRanges(rs []LineRange) []daemon.Range {
	out := make([]daemon.Range, 0, len(rs))
	for _, r := range rs {
		out = append(out, daemon.Range{Start: r.Start, End: r.End, ContentSHA: r.ContentSHA})
	}
	return out
}

func int64Ptr(v int64) *int64 { return &v }

// resolveSymlinks returns the canonical path with symlinks resolved.
// Falls back to the input if EvalSymlinks fails (e.g. for newly created paths).
func resolveSymlinks(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func findRepoRoot(filePath, fallbackCwd string) string {
	// Try `git rev-parse --show-toplevel` against the file's directory.
	dir := filepath.Dir(filePath)
	if root, ok := gitToplevel(dir); ok {
		return root
	}
	if fallbackCwd != "" {
		if root, ok := gitToplevel(fallbackCwd); ok {
			return root
		}
	}
	return ""
}

func gitToplevel(dir string) (string, bool) {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

func postToDaemon(payload daemon.EditPayload) error {
	port, err := daemon.ReadPort()
	if err != nil {
		// Daemon not running. The hook is best-effort — don't break Claude's flow.
		return nil
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	url := fmt.Sprintf("http://127.0.0.1:%d/edit", port)
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("daemon rejected: %s: %s", resp.Status, string(msg))
	}
	return nil
}
