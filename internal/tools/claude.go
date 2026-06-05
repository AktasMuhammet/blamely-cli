package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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

	// Cursor often omits transcript_path from the hook payload. Derive it from
	// cwd + session_id using the known storage layout:
	//   ~/.cursor/projects/<cwd-encoded>/agent-transcripts/<uuid>/<uuid>.jsonl
	// where <cwd-encoded> is the cwd with leading slash removed and / → -.
	if isCursor && p.TranscriptPath == "" && p.SessionID != "" && p.Cwd != "" {
		p.TranscriptPath = cursorTranscriptPath(p.Cwd, p.SessionID)
	}

	// Claude CLI also sometimes omits transcript_path. Derive it from cwd +
	// session_id using the Claude storage layout:
	//   ~/.claude/projects/<cwd-encoded>/<session-id>.jsonl
	// where <cwd-encoded> replaces ALL slashes (including the leading /) with -.
	if !isCursor && p.TranscriptPath == "" && p.SessionID != "" && p.Cwd != "" {
		p.TranscriptPath = claudeTranscriptPath(p.Cwd, p.SessionID)
	}

	filePath, ranges, suggested, err := extractClaudeRanges(p)
	if err != nil {
		return err
	}
	if filePath == "" {
		// No file-edit tool produced a path. A Bash command, however, may have
		// created or modified files directly (e.g. `printf > f`, `cat > f`,
		// heredocs, a script) — bypassing Write/Edit entirely. We can't parse
		// arbitrary shell (paths are often dynamic, e.g. `> "$fname"`), so we
		// ask git which source files in the repo just changed and attribute
		// those. Claude only (Cursor Tab has no Bash tool).
		if !isCursor && p.ToolName == "Bash" {
			gt := ReadTranscriptGenType(p.TranscriptPath)
			if gt == "" {
				gt = "chat"
			}
			return recordClaudeBashWrites(p.Cwd, p.SessionID, p.TranscriptPath, gt)
		}
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
	// For Claude CLI/Code: read the "entrypoint" field from the first user turn
	// of the transcript. "cli" and "sdk-ts" entrypoints map to GenTypeCLI;
	// interactive entrypoints (e.g. "claude-vscode") map to GenTypeChat.
	//
	// For Cursor we distinguish two sources:
	//   - Composer (chat): fires from within a conversation → session_id or
	//     conversation_id is set in the payload.
	//   - Cursor Tab (inline completion): no conversation context → both IDs
	//     are empty. When Cursor sets gen_type explicitly in the payload we
	//     honour that directly; otherwise we use the session-presence heuristic.
	genType := "chat"
	switch {
	case isCursor && p.GenType != "":
		genType = p.GenType
	case isCursor && p.SessionID == "" && p.ConversationID == "":
		genType = "completion"
	case !isCursor:
		genType = ReadTranscriptGenType(p.TranscriptPath)
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

	if isCursor && p.Model != "" {
		payload.Model = p.Model
	}
	applyHookUsage(&payload, hookUsageOptions{
		transcriptPath: p.TranscriptPath,
		sessionID:      p.SessionID,
		tool:           tool,
	})

	return postToDaemon(payload)
}

const (
	// bashWriteWindow bounds how recently a file must have been modified to be
	// credited to the Bash command that just ran. The PostToolUse hook fires
	// immediately after the command, so a generous window absorbs hook latency
	// while still excluding files the user edited minutes earlier.
	bashWriteWindow = 15 * time.Second
	// maxBashWriteFiles caps how many changed files we attribute to one command.
	// A larger change set is almost certainly a bulk operation (build, codegen,
	// `git checkout`, `npm install`) rather than authored content — we skip
	// rather than guess, to avoid stealing credit for non-authored files.
	maxBashWriteFiles = 30
)

// recordClaudeBashWrites attributes source files that changed while a Claude
// Bash command ran. It uses `git status` (not a filesystem walk) so the scan is
// bounded and automatically respects .gitignore — build output and node_modules
// never appear. Each changed source file is recorded as a medium-confidence
// claude edit covering the whole file, matching how a Write is stored, so the
// existing commit-time attribution credits it without any further changes.
func recordClaudeBashWrites(cwd, sessionID, transcriptPath, genType string) error {
	if cwd == "" {
		return nil
	}
	root, ok := gitToplevel(cwd)
	if !ok {
		return nil
	}
	files := recentlyChangedFiles(root, bashWriteWindow)
	// none → read-only command (ls/grep/test); too many → bulk op we won't guess.
	if len(files) == 0 || len(files) > maxBashWriteFiles {
		return nil
	}
	repoID, _ := gitutil.RepoID(resolveSymlinks(root))
	if repoID == "" {
		repoID = root
	}
	for _, rel := range files {
		abs := filepath.Join(root, rel)
		ranges := perLineShaRanges(abs)
		if len(ranges) == 0 {
			continue
		}
		payload := daemon.EditPayload{
			Tool:           "claude",
			Confidence:     "medium",
			GenType:        genType,
			RepoPath:       repoID,
			FilePath:       rel,
			SuggestedLines: int64(len(ranges)),
			Lines:          toDaemonRanges(ranges),
			RawMeta: fmt.Sprintf(`{"session_id":%q,"tool":"claude","source":"claude_bash_fswrite","transcript_path":%q}`,
				sessionID, transcriptPath),
		}
		// Backfill model + token usage from the session transcript, exactly like
		// the Write/Edit hook path — otherwise the row has an empty model.
		applyHookUsage(&payload, hookUsageOptions{
			transcriptPath: transcriptPath,
			sessionID:      sessionID,
			tool:           "claude",
		})
		if err := postToDaemon(payload); err != nil {
			return err
		}
	}
	return nil
}

// perLineShaRanges returns one range per non-blank line of the file, each
// carrying that line's content_sha (sha256 of the line text, sans trailing \r).
// The editor gutter attributes chat/cli rows by hashing the CURRENT line text
// and matching it against these shas, so a single whole-file hash would never
// paint a line — per-line shas are what the live gutter needs, and they also let
// commit-time attribution survive line-number drift. Capped so a huge generated
// file can't blow past the daemon's request size limit.
func perLineShaRanges(absPath string) []LineRange {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil
	}
	const maxLines = 4000
	var out []LineRange
	for i, ln := range strings.Split(string(data), "\n") {
		text := strings.TrimRight(ln, "\r")
		if strings.TrimSpace(text) == "" {
			continue
		}
		n := i + 1
		out = append(out, LineRange{Start: n, End: n, ContentSHA: sha256Hex([]byte(text))})
		if len(out) >= maxLines {
			break
		}
	}
	return out
}

// perLineShaRangesFromContent is perLineShaRanges for an in-memory string (the
// content an AI Write just produced) rather than a file on disk. It returns one
// {n,n} range per line. Non-blank lines carry a content_sha so attribution
// follows the line text across later insertions; blank lines carry no sha (a
// blank line's hash isn't unique) but still get a range so they're counted as
// authored. The trailing empty element from a final newline is dropped. Capped
// for request size, matching perLineShaRanges.
func perLineShaRangesFromContent(content string) []LineRange {
	if content == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1] // drop trailing empty from a final newline
	}
	const maxLines = 4000
	out := make([]LineRange, 0, len(lines))
	for i, ln := range lines {
		text := strings.TrimRight(ln, "\r")
		sha := ""
		if strings.TrimSpace(text) != "" {
			sha = sha256Hex([]byte(text))
		}
		n := i + 1
		out = append(out, LineRange{Start: n, End: n, ContentSHA: sha})
		if len(out) >= maxLines {
			break
		}
	}
	return out
}

// recentlyChangedFiles returns repo-relative paths that git reports as
// changed/untracked and whose on-disk mtime is within `window` of now. Using
// git keeps the result bounded (usually a handful of paths) and gitignore-aware.
func recentlyChangedFiles(root string, window time.Duration) []string {
	cmd := exec.Command("git", "-C", root, "status", "--porcelain", "--untracked-files=all")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	cutoff := time.Now().Add(-window)
	var files []string
	for _, line := range strings.Split(string(out), "\n") {
		// Porcelain v1 lines are "XY <path>" (3-char status prefix). Renames
		// and copies use "XY <old> -> <new>"; we want the new path.
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[3:])
		if i := strings.Index(path, " -> "); i >= 0 {
			path = path[i+len(" -> "):]
		}
		path = strings.Trim(path, `"`)
		if path == "" {
			continue
		}
		abs := filepath.Join(root, path)
		info, err := os.Stat(abs)
		if err != nil || info.IsDir() || info.Size() == 0 {
			continue
		}
		if info.ModTime().Before(cutoff) {
			continue
		}
		if !looksLikeSourceFile(abs) {
			continue
		}
		files = append(files, path)
	}
	return files
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
		// Emit ONE range per line, each non-blank line carrying its own
		// content_sha. This is what makes attribution survive line drift: when
		// the user later inserts a line into the AI-written file, each AI line is
		// re-located by hashing the CURRENT text and matching the stored sha at
		// its new position — so the AI lines stay AI even after they shift, and
		// the user's freshly-typed line (no matching sha) is correctly human.
		//
		// A single whole-file range [1, N] (or the old 1<<30 sentinel) can't do
		// this: it matches purely by line number, so an inserted line lands
		// inside the range and is mislabelled AI, while the AI line pushed past N
		// falls outside and is mislabelled human. Per-line shas fix both.
		ranges := perLineShaRangesFromContent(in.Content)
		if len(ranges) == 0 {
			return in.FilePath, nil, suggested, nil
		}
		return in.FilePath, ranges, suggested, nil

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

// cursorTranscriptPath derives the Cursor agent-transcript JSONL path for a
// given project working directory and session UUID. Cursor stores transcripts at:
//   ~/.cursor/projects/<cwd-encoded>/agent-transcripts/<uuid>/<uuid>.jsonl
// where <cwd-encoded> is the cwd with leading slash removed and / replaced by -.
// Returns "" if the file doesn't exist yet (e.g. session hasn't been written).
func cursorTranscriptPath(cwd, sessionID string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	proj := strings.TrimPrefix(filepath.ToSlash(cwd), "/")
	proj = strings.ReplaceAll(proj, "/", "-")
	p := filepath.Join(home, ".cursor", "projects", proj, "agent-transcripts", sessionID, sessionID+".jsonl")
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// claudeTranscriptPath derives the Claude CLI/Code transcript JSONL path for a
// given project working directory and session UUID. Claude stores transcripts at:
//   ~/.claude/projects/<cwd-encoded>/<session-id>.jsonl
// where <cwd-encoded> replaces ALL slashes (including the leading /) with -.
// Returns "" if the file doesn't exist.
func claudeTranscriptPath(cwd, sessionID string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	proj := strings.ReplaceAll(filepath.ToSlash(cwd), "/", "-")
	p := filepath.Join(home, ".claude", "projects", proj, sessionID+".jsonl")
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

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
