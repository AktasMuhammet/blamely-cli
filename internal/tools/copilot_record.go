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
		RawMeta: fmt.Sprintf(`{"session_id":%q,"tool":%q,"source":"copilot_hook"}`,
			p.SessionID, p.ToolName),
	}
	return postToDaemon(payload)
}

// extractCopilotRanges is intentionally permissive: it accepts Edit / Write /
// MultiEdit shapes (same as Claude) and any payload whose tool_input has a
// usable `file_path` + `new_string`/`content`.
func extractCopilotRanges(p copilotHookPayload) (string, []LineRange, int64) {
	switch p.ToolName {
	case "Edit":
		var in editInput
		if err := json.Unmarshal(p.ToolInput, &in); err != nil || in.FilePath == "" {
			return "", nil, 0
		}
		suggested := int64(countLines(in.NewString))
		lr, _ := LocateNewString(in.FilePath, in.NewString)
		if lr == nil {
			return in.FilePath, nil, suggested
		}
		return in.FilePath, []LineRange{*lr}, suggested

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
			suggested += int64(countLines(ed.NewString))
			if lr, err := LocateNewString(in.FilePath, ed.NewString); err == nil && lr != nil {
				out = append(out, *lr)
			}
		}
		return in.FilePath, out, suggested

	default:
		// Generic fallback: try to read {file_path, new_string|content} out of
		// tool_input regardless of tool_name. Many Copilot payload variants
		// land here.
		var generic struct {
			FilePath  string `json:"file_path"`
			NewString string `json:"new_string"`
			Content   string `json:"content"`
		}
		if err := json.Unmarshal(p.ToolInput, &generic); err == nil && generic.FilePath != "" {
			body := generic.NewString
			if body == "" {
				body = generic.Content
			}
			suggested := int64(countLines(body))
			if body != "" {
				if lr, _ := LocateNewString(generic.FilePath, body); lr != nil {
					return generic.FilePath, []LineRange{*lr}, suggested
				}
			}
			return generic.FilePath, nil, suggested
		}
		return "", nil, 0
	}
}

func copilotGenType(toolName string) string {
	t := strings.ToLower(toolName)
	if strings.Contains(t, "chat") || strings.Contains(t, "ask") || strings.Contains(t, "panel") {
		return "chat"
	}
	// Default for Copilot's Edit/Write/Apply etc. is inline completion.
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
