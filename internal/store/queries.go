package store

import (
	"database/sql"
	"fmt"
	"strings"
)

type Tool string

const (
	ToolClaude    Tool = "claude"
	ToolCursor    Tool = "cursor"
	ToolCodex     Tool = "codex"
	ToolCopilot   Tool = "copilot"
	ToolHuman     Tool = "human"
	// ToolCopyPaste marks content that arrived via a clipboard paste rather
	// than being typed. We don't claim AI origin — the source could be a
	// web AI chat, another project, Stack Overflow, etc. The signal is
	// "this code was pasted, not typed", which is itself useful in reports
	// and stops blamely from confidently labelling pasted code as human-typed.
	ToolCopyPaste Tool = "copypaste"
)

type Confidence string

const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
	ConfidenceLow    Confidence = "low"
)

// GenType describes how an AI edit was produced.
type GenType string

const (
	GenTypeChat       GenType = "chat"       // Conversational AI session (Claude Code, Cursor Composer, Copilot Chat)
	GenTypeCLI        GenType = "cli"        // Command-line tool (Codex CLI, claude CLI)
	GenTypeCompletion GenType = "completion" // Inline/tab completion (Copilot Tab, Cursor Tab)
	GenTypeUnknown    GenType = "unknown"
)

type Edit struct {
	ID               int64
	TimestampNanos   int64
	RepoPath         string
	FilePath         string
	Tool             Tool
	Confidence       Confidence
	GenType          GenType
	Model            sql.NullString
	InputTokens      sql.NullInt64
	OutputTokens     sql.NullInt64
	CacheReadTokens  sql.NullInt64
	CacheWriteTokens sql.NullInt64
	HashBefore       sql.NullString
	HashAfter        sql.NullString
	RawMeta          sql.NullString
	// SuggestedLines is the AI's original suggestion size at watcher time.
	// AcceptedLines() returns the sum of (EndLine-StartLine+1) across Lines,
	// i.e. what actually stuck after any partial-acceptance/user-editing.
	SuggestedLines int64
	Lines          []EditLine
}

// AcceptedLines returns the total number of lines covered by this edit's
// post-acceptance line ranges. Compare to SuggestedLines for an acceptance
// ratio: e.g. SuggestedLines=10, AcceptedLines()=6 means the user kept 6.
func (e *Edit) AcceptedLines() int64 {
	var n int64
	for _, l := range e.Lines {
		if l.EndLine >= l.StartLine {
			n += int64(l.EndLine - l.StartLine + 1)
		}
	}
	return n
}

type EditLine struct {
	StartLine  int
	EndLine    int
	ContentSHA string
}

func (db *DB) InsertEdit(e Edit) (int64, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	gt := string(e.GenType)
	if gt == "" {
		gt = string(GenTypeUnknown)
	}
	res, err := tx.Exec(`
		INSERT INTO edits(ts, repo_path, file_path, tool, confidence, gen_type,
			model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
			hash_before, hash_after, raw_meta, suggested_lines)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.TimestampNanos, e.RepoPath, e.FilePath, string(e.Tool), string(e.Confidence), gt,
		nullableString(e.Model), nullableInt(e.InputTokens), nullableInt(e.OutputTokens),
		nullableInt(e.CacheReadTokens), nullableInt(e.CacheWriteTokens),
		nullableString(e.HashBefore), nullableString(e.HashAfter), nullableString(e.RawMeta),
		e.SuggestedLines,
	)
	if err != nil {
		return 0, fmt.Errorf("insert edit: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("last insert id: %w", err)
	}
	for _, ln := range e.Lines {
		if _, err := tx.Exec(`
			INSERT INTO edit_lines(edit_id, start_line, end_line, content_sha)
			VALUES (?, ?, ?, ?)`,
			id, ln.StartLine, ln.EndLine, ln.ContentSHA,
		); err != nil {
			return 0, fmt.Errorf("insert edit_line: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return id, nil
}

// EditsForFileSince returns edits touching repo/file with ts >= sinceNanos,
// newest first. Used by the attribution join.
func (db *DB) EditsForFileSince(repo, file string, sinceNanos int64) ([]Edit, error) {
	rows, err := db.Query(`
		SELECT id, ts, repo_path, file_path, tool, confidence, gen_type,
			model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
			hash_before, hash_after, raw_meta, suggested_lines
		FROM edits
		WHERE repo_path = ? AND file_path = ? AND ts >= ?
		ORDER BY ts DESC`,
		repo, file, sinceNanos,
	)
	if err != nil {
		return nil, fmt.Errorf("query edits: %w", err)
	}
	defer rows.Close()

	var out []Edit
	for rows.Next() {
		var e Edit
		var tool, conf, genType string
		if err := rows.Scan(&e.ID, &e.TimestampNanos, &e.RepoPath, &e.FilePath, &tool, &conf, &genType,
			&e.Model, &e.InputTokens, &e.OutputTokens, &e.CacheReadTokens, &e.CacheWriteTokens,
			&e.HashBefore, &e.HashAfter, &e.RawMeta, &e.SuggestedLines,
		); err != nil {
			return nil, fmt.Errorf("scan edit: %w", err)
		}
		e.Tool = Tool(tool)
		e.Confidence = Confidence(conf)
		e.GenType = GenType(genType)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		lines, err := db.linesForEdit(out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Lines = lines
	}
	return out, nil
}

func (db *DB) linesForEdit(editID int64) ([]EditLine, error) {
	rows, err := db.Query(`SELECT start_line, end_line, COALESCE(content_sha, '')
		FROM edit_lines WHERE edit_id = ? ORDER BY start_line`, editID)
	if err != nil {
		return nil, fmt.Errorf("query edit_lines: %w", err)
	}
	defer rows.Close()
	var out []EditLine
	for rows.Next() {
		var ln EditLine
		if err := rows.Scan(&ln.StartLine, &ln.EndLine, &ln.ContentSHA); err != nil {
			return nil, fmt.Errorf("scan edit_line: %w", err)
		}
		out = append(out, ln)
	}
	return out, rows.Err()
}

// LatestCopilotModelNear returns the model string from the most recent
// copilot row whose timestamp falls inside [ts-window, ts+window] AND that
// has a non-null model. Returns "" when no such row exists. Used by the
// HumanEditWatcher so an in-editor paste that we attribute to Copilot can
// be tagged with the model the user actually had selected in the chat
// panel (recorded by CopilotChatWatcher's session-file parse).
func (db *DB) LatestCopilotModelNear(tsNanos, windowNanos int64) string {
	row := db.QueryRow(`SELECT model FROM edits
		WHERE tool='copilot' AND model IS NOT NULL AND model != ''
		  AND ts >= ? AND ts <= ?
		ORDER BY ts DESC LIMIT 1`,
		tsNanos-windowNanos, tsNanos+windowNanos)
	var model sql.NullString
	if err := row.Scan(&model); err != nil {
		return ""
	}
	if !model.Valid {
		return ""
	}
	return model.String
}

// LatestCopilotGenTypeNear returns the gen_type to attribute a file change
// to when Copilot is active in the [ts-window, ts+window] interval.
//
// Resolution order:
//   1. If ANY chat marker exists in the window, return "chat". Chat is the
//      more specific signal — it requires either a chat-session JSONL
//      response chunk or a Copilot extension log line with "chat" in it,
//      neither of which fire for an inline Tab accept.
//   2. Otherwise return the gen_type of the most recent specific marker
//      (skipping "unknown" rows from the globalStorage-only signal).
//   3. Returns "" when no row matches; callers default accordingly
//      (humanedit treats "" as "completion" since inline Tab is the
//      common case when no more-specific signal exists).
func (db *DB) LatestCopilotGenTypeNear(tsNanos, windowNanos int64) string {
	from, to := tsNanos-windowNanos, tsNanos+windowNanos
	// Step 1: chat-preferred.
	row := db.QueryRow(`SELECT 1 FROM edits
		WHERE tool='copilot' AND gen_type='chat' AND ts >= ? AND ts <= ?
		LIMIT 1`, from, to)
	var dummy int
	if err := row.Scan(&dummy); err == nil {
		return "chat"
	}
	// Step 2: latest specific marker.
	row = db.QueryRow(`SELECT gen_type FROM edits
		WHERE tool='copilot' AND gen_type IS NOT NULL AND gen_type != '' AND gen_type != 'unknown'
		  AND ts >= ? AND ts <= ?
		ORDER BY ts DESC LIMIT 1`, from, to)
	var gt sql.NullString
	if err := row.Scan(&gt); err != nil {
		return ""
	}
	if !gt.Valid {
		return ""
	}
	return gt.String
}

// HasCopilotSessionNear returns true when at least one copilot session-active
// marker exists in the DB with a timestamp inside [ts-window, ts+window].
// Used by the attribution step to fold in Copilot attribution for lines that
// have no other AI edit record.
func (db *DB) HasCopilotSessionNear(tsNanos, windowNanos int64) bool {
	row := db.QueryRow(`SELECT COUNT(*) FROM edits
		WHERE tool='copilot' AND ts >= ? AND ts <= ?`,
		tsNanos-windowNanos, tsNanos+windowNanos)
	var count int
	_ = row.Scan(&count)
	return count > 0
}

// KnownCommits returns all noted commits for the given repos, ordered by ts desc.
// If sinceNanos > 0, only commits with ts >= sinceNanos are returned.
func (db *DB) KnownCommits(repos []string, sinceNanos int64) ([]CommitRow, error) {
	if len(repos) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(repos))
	args := make([]any, len(repos))
	for i, r := range repos {
		placeholders[i] = "?"
		args[i] = r
	}
	cond := "repo_path IN (" + strings.Join(placeholders, ",") + ") AND note_written=1"
	if sinceNanos > 0 {
		cond += " AND ts >= ?"
		args = append(args, sinceNanos)
	}
	rows, err := db.Query("SELECT sha, repo_path, ts FROM commits WHERE "+cond+" ORDER BY ts DESC", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CommitRow
	for rows.Next() {
		var r CommitRow
		if err := rows.Scan(&r.SHA, &r.RepoPath, &r.TS); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type CommitRow struct {
	SHA      string
	RepoPath string
	TS       int64 // unix nanos
}

// SessionDurationNanos estimates how long the user worked before a commit by
// looking at the earliest edit in the DB that contributed to that commit.
// Specifically it finds min(edits.ts) for the given repo where ts <= commitNanos,
// then returns commitNanos - min(ts). Returns 0 if no edits are found.
func (db *DB) SessionDurationNanos(repoPath string, commitNanos int64) int64 {
	const lookback = int64(8 * 60 * 60 * 1e9) // 8-hour max session window
	row := db.QueryRow(`
		SELECT COALESCE(MIN(ts), 0) FROM edits
		WHERE repo_path = ? AND ts <= ? AND ts >= ? AND file_path != ''`,
		repoPath, commitNanos, commitNanos-lookback,
	)
	var minTS int64
	_ = row.Scan(&minTS)
	if minTS == 0 {
		return 0
	}
	return commitNanos - minTS
}

// KnownRepoPaths returns all distinct repo_path values from the edits table.
// Used by the VelocityWatcher to know which dirs to watch with fsnotify.
func (db *DB) KnownRepoPaths() ([]string, error) {
	rows, err := db.Query(`SELECT DISTINCT repo_path FROM edits WHERE repo_path != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (db *DB) MarkCommitNoted(sha, repo string, tsNanos int64) error {
	_, err := db.Exec(`INSERT INTO commits(sha, repo_path, ts, note_written)
		VALUES (?, ?, ?, 1)
		ON CONFLICT(sha) DO UPDATE SET note_written = 1`,
		sha, repo, tsNanos)
	if err != nil {
		return fmt.Errorf("mark commit: %w", err)
	}
	return nil
}

func nullableString(s sql.NullString) any {
	if s.Valid {
		return s.String
	}
	return nil
}

func nullableInt(i sql.NullInt64) any {
	if i.Valid {
		return i.Int64
	}
	return nil
}
