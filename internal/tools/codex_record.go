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

// codexHookPayload is the JSON Codex CLI sends on stdin for PostToolUse hooks.
type codexHookPayload struct {
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	Cwd            string          `json:"cwd"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	Model          string          `json:"model"`
}

// RecordCodexFromStdin handles `blamely record codex` (Codex CLI PostToolUse).
func RecordCodexFromStdin(r io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(r, 8<<20))
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	var p codexHookPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		log.Printf("blamely record codex: parse hook payload: %v", err)
		return nil
	}

	filePath, ranges, suggested := extractCodexHookRanges(p)
	if filePath == "" {
		return nil
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

	payload := daemon.EditPayload{
		Tool:           "codex",
		Confidence:     "high",
		GenType:        "cli",
		RepoPath:       repoPath,
		FilePath:       rel,
		Model:          p.Model,
		SuggestedLines: suggested,
		Lines:          toDaemonRanges(ranges),
		RawMeta: fmt.Sprintf(`{"session_id":%q,"tool":%q,"transcript_path":%q,"source":"codex_hook"}`,
			p.SessionID, p.ToolName, p.TranscriptPath),
	}
	applyHookUsage(&payload, hookUsageOptions{
		transcriptPath: p.TranscriptPath,
		sessionID:      p.SessionID,
		tool:           "codex",
	})
	return postToDaemon(payload)
}

func extractCodexHookRanges(p codexHookPayload) (string, []LineRange, int64) {
	name := strings.ToLower(p.ToolName)
	if looksLikePatch(name) {
		var suggested int64
		var out []LineRange
		var primary string
		for _, f := range parsePatchFiles(p.ToolInput) {
			if primary == "" {
				primary = f.Path
			}
			out = append(out, LineRange{Start: f.StartLine, End: f.EndLine, ContentSHA: f.ContentSHA})
			if f.EndLine >= f.StartLine {
				suggested += int64(f.EndLine - f.StartLine + 1)
			}
		}
		if primary == "" {
			return "", nil, 0
		}
		return primary, out, suggested
	}

	// Claude-compatible Edit/Write/MultiEdit shapes (some Codex versions).
	cl := claudeHookPayload{ToolName: p.ToolName, ToolInput: p.ToolInput}
	fp, ranges, suggested, err := extractClaudeRanges(cl)
	if err != nil {
		return "", nil, 0
	}
	return fp, ranges, suggested
}
