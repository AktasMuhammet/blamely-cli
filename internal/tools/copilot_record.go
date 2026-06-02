package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strings"

	"github.com/blamely/blamely/internal/daemon"
	"github.com/blamely/blamely/internal/gitutil"
)

// copilotHookPayload is a tolerant view of the JSON the GitHub Copilot CLI
// hook pipes into `blamely record copilot`. The Copilot framework uses the
// same Anthropic-style PostToolUse shape as Claude / Cursor (tool_name +
// tool_input with file_path), so we share the editInput/writeInput/multiEdit
// structs from claude.go.
//
// Fields we don't recognise are ignored. When the payload doesn't carry a
// file path we still emit a low-confidence session-active marker so the
// fold-in heuristic in attribute.go can credit Copilot at all rather than
// leaving the lines as human.
type copilotHookPayload struct {
	SessionID      string          `json:"session_id"`
	ConversationID string          `json:"conversation_id"`
	TranscriptPath string          `json:"transcript_path"`
	Cwd            string          `json:"cwd"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	Model          string          `json:"model"`
}

// RecordCopilotFromStdin handles the PostToolUse hook payload Copilot pipes
// to `blamely record copilot`. It mirrors RecordClaudeFromStdin but:
//   - records tool="copilot"
//   - uses "completion" as the default gen_type (Copilot is mostly inline/tab),
//     downgrades to "chat" when the payload's tool_name suggests a chat panel
//   - falls back to a session-active marker (no file/lines) for payload shapes
//     we don't recognise, so the attribute fold-in can still credit Copilot
func RecordCopilotFromStdin(r io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(r, 8<<20))
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	var p copilotHookPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		// Malformed JSON: never fail the host tool. Log and emit a marker.
		log.Printf("blamely record copilot: parse hook payload: %v", err)
		return emitCopilotMarker("")
	}
	if p.SessionID == "" && p.ConversationID != "" {
		p.SessionID = p.ConversationID
	}

	filePath, ranges, suggested := extractCopilotRanges(p)
	if filePath == "" {
		// Payload didn't carry a file path: keep the session-marker fallback.
		return emitCopilotMarker(p.SessionID)
	}

	resolved := resolveSymlinks(filePath)
	repoPath, _ := gitutil.RepoID(resolved)
	if repoPath == "" && p.Cwd != "" {
		repoPath, _ = gitutil.RepoID(resolveSymlinks(p.Cwd))
	}
	wt, _ := gitutil.Toplevel(resolved)
	rel := resolved
	if wt != "" {
		if r, err := filepath.Rel(wt, resolved); err == nil && !strings.HasPrefix(r, "..") {
			rel = r
		}
	}

	gen := copilotGenType(p.ToolName)
	payload := daemon.EditPayload{
		Tool:           "copilot",
		Confidence:     "high", // we have a real file+lines, not a session guess
		GenType:        gen,
		RepoPath:       repoPath,
		FilePath:       rel,
		Model:          p.Model,
		SuggestedLines: suggested,
		Lines:          toDaemonRanges(ranges),
		RawMeta: fmt.Sprintf(`{"session_id":%q,"tool":%q,"transcript_path":%q,"source":"copilot_hook"}`,
			p.SessionID, p.ToolName, p.TranscriptPath),
	}
	applyHookUsage(&payload, hookUsageOptions{
		transcriptPath: p.TranscriptPath,
		sessionID:      p.SessionID,
		tool:           "copilot",
	})
	return postToDaemon(payload)
}

// extractCopilotRanges is intentionally permissive: it accepts Copilot's native
// agent tool shapes (str_replace_editor, create_file, insert_edit_into_file) as
// well as Claude-compatible shapes (Edit/Write/MultiEdit) and a generic fallback
// that tries any payload with a recognisable file path + content field.
func extractCopilotRanges(p copilotHookPayload) (string, []LineRange, int64) {
	switch p.ToolName {
	// ── GitHub Copilot agent / chat tools ────────────────────────────────────
	// These are the tool names Copilot sends from its chat panel in VS Code and
	// Cursor. The payloads use "path" not "file_path", and field names differ
	// from Claude's conventions.

	case "str_replace_editor":
		// Payload: {command, path, old_str, new_str} for replacements, or
		//          {command, path, content} for "create" operations.
		var in struct {
			Command string `json:"command"`
			Path    string `json:"path"`
			OldStr  string `json:"old_str"`
			NewStr  string `json:"new_str"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(p.ToolInput, &in); err != nil || in.Path == "" {
			return "", nil, 0
		}
		body := in.NewStr
		if body == "" {
			body = in.Content
		}
		if strings.TrimSpace(body) == "" && in.OldStr != "" {
			return in.Path, nil, int64(countLines(in.OldStr))
		}
		lr, _ := LocateNewString(in.Path, body)
		if lr == nil {
			return in.Path, nil, CountAddedLines(in.OldStr, body)
		}
		ranges, suggested := narrowToChangedLines(in.OldStr, body, *lr)
		return in.Path, ranges, suggested

	case "create_file":
		// Payload: {path, content}
		var in struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(p.ToolInput, &in); err != nil || in.Path == "" {
			return "", nil, 0
		}
		suggested := int64(countLines(in.Content))
		lr, _ := LineRangeForWholeFile(in.Path)
		if lr == nil {
			return in.Path, nil, suggested
		}
		return in.Path, []LineRange{*lr}, suggested

	case "insert_edit_into_file":
		// Payload: {path, code, explanation?}
		var in struct {
			Path string `json:"path"`
			Code string `json:"code"`
		}
		if err := json.Unmarshal(p.ToolInput, &in); err != nil || in.Path == "" {
			return "", nil, 0
		}
		suggested := int64(countLines(in.Code))
		lr, _ := LocateNewString(in.Path, in.Code)
		if lr == nil {
			return in.Path, nil, suggested
		}
		return in.Path, []LineRange{*lr}, suggested

	// ── Claude-compatible shapes (also used by some Copilot variants) ─────────

	case "Edit":
		var in editInput
		if err := json.Unmarshal(p.ToolInput, &in); err != nil || in.FilePath == "" {
			return "", nil, 0
		}
		if strings.TrimSpace(in.NewString) == "" && in.OldString != "" {
			return in.FilePath, nil, int64(countLines(in.OldString))
		}
		lr, _ := LocateNewString(in.FilePath, in.NewString)
		if lr == nil {
			return in.FilePath, nil, CountAddedLines(in.OldString, in.NewString)
		}
		ranges, suggested := narrowToChangedLines(in.OldString, in.NewString, *lr)
		return in.FilePath, ranges, suggested

	case "Write":
		var in writeInput
		if err := json.Unmarshal(p.ToolInput, &in); err != nil || in.FilePath == "" {
			return "", nil, 0
		}
		suggested := int64(countLines(in.Content))
		lr, _ := LineRangeForWholeFile(in.FilePath)
		if lr == nil {
			return in.FilePath, nil, suggested
		}
		return in.FilePath, []LineRange{*lr}, suggested

	case "MultiEdit":
		var in multiEditInput
		if err := json.Unmarshal(p.ToolInput, &in); err != nil || in.FilePath == "" {
			return "", nil, 0
		}
		var suggested int64
		var out []LineRange
		for _, ed := range in.Edits {
			if strings.TrimSpace(ed.NewString) == "" && ed.OldString != "" {
				suggested += int64(countLines(ed.OldString))
				continue
			}
			lr, err := LocateNewString(in.FilePath, ed.NewString)
			if err != nil || lr == nil {
				suggested += CountAddedLines(ed.OldString, ed.NewString)
				continue
			}
			narrowed, narrowSuggest := narrowToChangedLines(ed.OldString, ed.NewString, *lr)
			out = append(out, narrowed...)
			suggested += narrowSuggest
		}
		return in.FilePath, out, suggested

	default:
		// Generic fallback: try multiple field-name conventions. Copilot uses
		// "path"; Claude/older hooks use "file_path". Content body may be in
		// "new_string", "new_str", "content", or "code".
		var generic struct {
			FilePath  string `json:"file_path"`
			Path      string `json:"path"`
			NewString string `json:"new_string"`
			NewStr    string `json:"new_str"`
			Content   string `json:"content"`
			Code      string `json:"code"`
		}
		if err := json.Unmarshal(p.ToolInput, &generic); err != nil {
			return "", nil, 0
		}
		fp := generic.FilePath
		if fp == "" {
			fp = generic.Path
		}
		if fp == "" {
			return "", nil, 0
		}
		body := generic.NewString
		if body == "" {
			body = generic.NewStr
		}
		if body == "" {
			body = generic.Content
		}
		if body == "" {
			body = generic.Code
		}
		suggested := int64(countLines(body))
		if body != "" {
			if lr, _ := LocateNewString(fp, body); lr != nil {
				return fp, []LineRange{*lr}, suggested
			}
		}
		return fp, nil, suggested
	}
}

func copilotGenType(toolName string) string {
	// Copilot's native agent/chat tool names — always produced by the chat panel.
	switch toolName {
	case "str_replace_editor", "create_file", "insert_edit_into_file":
		return "chat"
	}
	t := strings.ToLower(toolName)
	if strings.Contains(t, "chat") || strings.Contains(t, "ask") || strings.Contains(t, "panel") ||
		strings.Contains(t, "insert") || strings.Contains(t, "create") {
		return "chat"
	}
	// Default for inline tab-completion tools (Edit/Write/Apply etc.).
	return "completion"
}

// emitCopilotMarker is a fallback for payloads where we couldn't extract a
// file path. We log and return nil rather than POSTing — the daemon's
// CopilotWatcher already produces session-active markers via its in-process
// sink (which bypasses the HTTP validation that requires repo_path /
// file_path), so a parallel HTTP marker would just get rejected.
func emitCopilotMarker(sessionID string) error {
	log.Printf("blamely record copilot: payload missing file_path (session=%q) — relying on watcher's session marker", sessionID)
	_ = daemon.EditPayload{} // keep daemon import in case the policy changes
	return nil
}
