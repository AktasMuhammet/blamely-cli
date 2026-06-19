package store

import (
	"fmt"
	"time"
)

// SessionUsage is a tool's CUMULATIVE token spend for one (session, model),
// reported at session end. It's a session-level metric, not per-edit: it
// includes turns that produced no file change, so it must never be folded into
// an individual edit's token columns.
type SessionUsage struct {
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	ReasoningTokens  int64
}

// SaveSessionUsage upserts the cumulative usage for (sessionID, tool, model).
// Idempotent: re-reading the same session.shutdown overwrites rather than sums,
// since the reported totals are already cumulative for the whole session.
func (db *DB) SaveSessionUsage(sessionID, tool, model string, u SessionUsage) error {
	_, err := db.Exec(
		`INSERT INTO session_usage
		   (session_id, tool, model, input_tokens, output_tokens,
		    cache_read_tokens, cache_write_tokens, reasoning_tokens, updated_nanos)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(session_id, tool, model) DO UPDATE SET
		   input_tokens       = excluded.input_tokens,
		   output_tokens      = excluded.output_tokens,
		   cache_read_tokens  = excluded.cache_read_tokens,
		   cache_write_tokens = excluded.cache_write_tokens,
		   reasoning_tokens   = excluded.reasoning_tokens,
		   updated_nanos      = excluded.updated_nanos`,
		sessionID, tool, model, u.InputTokens, u.OutputTokens,
		u.CacheReadTokens, u.CacheWriteTokens, u.ReasoningTokens, time.Now().UnixNano(),
	)
	if err != nil {
		return fmt.Errorf("save session usage %s/%s/%s: %w", sessionID, tool, model, err)
	}
	return nil
}

// SessionUsageRow is one (session, tool, model) usage record plus its identity,
// for listing in reports/status.
type SessionUsageRow struct {
	SessionID string
	Tool      string
	Model     string
	Usage     SessionUsage
}

// RecentSessionUsage returns the most-recently-updated session usage rows, newest
// first, capped at limit (<=0 means a default of 20).
func (db *DB) RecentSessionUsage(limit int) ([]SessionUsageRow, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := db.Query(
		`SELECT session_id, tool, model, input_tokens, output_tokens,
		        cache_read_tokens, cache_write_tokens, reasoning_tokens
		 FROM session_usage ORDER BY updated_nanos DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("recent session usage: %w", err)
	}
	defer rows.Close()
	var out []SessionUsageRow
	for rows.Next() {
		var r SessionUsageRow
		if err := rows.Scan(&r.SessionID, &r.Tool, &r.Model,
			&r.Usage.InputTokens, &r.Usage.OutputTokens,
			&r.Usage.CacheReadTokens, &r.Usage.CacheWriteTokens, &r.Usage.ReasoningTokens); err != nil {
			return nil, fmt.Errorf("scan session usage: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LoadSessionUsage returns the saved usage for (sessionID, tool, model), ok=false
// if none recorded yet.
func (db *DB) LoadSessionUsage(sessionID, tool, model string) (SessionUsage, bool) {
	var u SessionUsage
	err := db.QueryRow(
		`SELECT input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens
		 FROM session_usage WHERE session_id = ? AND tool = ? AND model = ?`,
		sessionID, tool, model,
	).Scan(&u.InputTokens, &u.OutputTokens, &u.CacheReadTokens, &u.CacheWriteTokens, &u.ReasoningTokens)
	if err != nil {
		return SessionUsage{}, false
	}
	return u, true
}
