package store

import (
	"database/sql"
	"fmt"

	"github.com/google/uuid"
)

// migrateWorkSessionsUUID converts work-session ids from INTEGER autoincrement
// to TEXT UUIDs (sessions.id and edits.session_id). Idempotent.
func (db *DB) migrateWorkSessionsUUID() error {
	if sessionsIDIsText(db) {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT id, repo_path, branch, COALESCE(base_sha, '') FROM sessions`)
	if err != nil {
		return fmt.Errorf("read sessions: %w", err)
	}
	idMap := map[int64]string{}
	type oldSession struct {
		uuid, repo, branch, base string
	}
	var migrated []oldSession
	for rows.Next() {
		var oldID int64
		var repo, branch, base string
		if err := rows.Scan(&oldID, &repo, &branch, &base); err != nil {
			rows.Close()
			return err
		}
		u := uuid.New().String()
		idMap[oldID] = u
		migrated = append(migrated, oldSession{u, repo, branch, base})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	if _, err := tx.Exec(`
		CREATE TABLE sessions_new (
			id        TEXT PRIMARY KEY NOT NULL,
			repo_path TEXT NOT NULL,
			branch    TEXT NOT NULL,
			base_sha  TEXT NOT NULL DEFAULT ''
		)`); err != nil {
		return fmt.Errorf("create sessions_new: %w", err)
	}
	for _, s := range migrated {
		if _, err := tx.Exec(
			`INSERT INTO sessions_new(id, repo_path, branch, base_sha) VALUES (?, ?, ?, ?)`,
			s.uuid, s.repo, s.branch, s.base,
		); err != nil {
			return fmt.Errorf("insert session_new: %w", err)
		}
	}

	if _, err := tx.Exec(`
		CREATE TABLE edits_new (
			id                  INTEGER PRIMARY KEY AUTOINCREMENT,
			ts                  INTEGER NOT NULL,
			repo_path           TEXT NOT NULL,
			file_path           TEXT NOT NULL,
			tool                TEXT NOT NULL,
			confidence          TEXT NOT NULL,
			gen_type            TEXT NOT NULL DEFAULT 'unknown',
			model               TEXT,
			input_tokens        INTEGER,
			output_tokens       INTEGER,
			cache_read_tokens   INTEGER,
			cache_write_tokens  INTEGER,
			hash_before         TEXT,
			hash_after          TEXT,
			raw_meta            TEXT,
			suggested_lines     INTEGER NOT NULL DEFAULT 0,
			branch              TEXT,
			session_id          TEXT
		)`); err != nil {
		return fmt.Errorf("create edits_new: %w", err)
	}

	editRows, err := tx.Query(`
		SELECT id, ts, repo_path, file_path, tool, confidence, gen_type,
			model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
			hash_before, hash_after, raw_meta, suggested_lines, branch, session_id
		FROM edits`)
	if err != nil {
		return fmt.Errorf("read edits: %w", err)
	}
	for editRows.Next() {
		var e Edit
		var tool, conf, gt string
		var branch sql.NullString
		var oldSID sql.NullInt64
		if err := editRows.Scan(
			&e.ID, &e.TimestampNanos, &e.RepoPath, &e.FilePath, &tool, &conf, &gt,
			&e.Model, &e.InputTokens, &e.OutputTokens, &e.CacheReadTokens, &e.CacheWriteTokens,
			&e.HashBefore, &e.HashAfter, &e.RawMeta, &e.SuggestedLines, &branch, &oldSID,
		); err != nil {
			editRows.Close()
			return err
		}
		var sid any
		if oldSID.Valid {
			if u, ok := idMap[oldSID.Int64]; ok {
				sid = u
			}
		}
		if _, err := tx.Exec(`
			INSERT INTO edits_new(
				id, ts, repo_path, file_path, tool, confidence, gen_type,
				model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
				hash_before, hash_after, raw_meta, suggested_lines, branch, session_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.ID, e.TimestampNanos, e.RepoPath, e.FilePath, tool, conf, gt,
			nullableString(e.Model), nullableInt(e.InputTokens), nullableInt(e.OutputTokens),
			nullableInt(e.CacheReadTokens), nullableInt(e.CacheWriteTokens),
			nullableString(e.HashBefore), nullableString(e.HashAfter), nullableString(e.RawMeta),
			e.SuggestedLines, nullableNonEmpty(branch.String), sid,
		); err != nil {
			editRows.Close()
			return fmt.Errorf("insert edit_new: %w", err)
		}
	}
	editRows.Close()
	if err := editRows.Err(); err != nil {
		return err
	}

	if _, err := tx.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE edits`); err != nil {
		return fmt.Errorf("drop edits: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE edits_new RENAME TO edits`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE sessions`); err != nil {
		return fmt.Errorf("drop sessions: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE sessions_new RENAME TO sessions`); err != nil {
		return err
	}
	for _, idx := range []string{
		`CREATE INDEX IF NOT EXISTS edits_repo_file_ts ON edits(repo_path, file_path, ts)`,
		`CREATE INDEX IF NOT EXISTS edits_repo_branch ON edits(repo_path, branch)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS sessions_repo_branch_base ON sessions(repo_path, branch, base_sha)`,
	} {
		if _, err := tx.Exec(idx); err != nil {
			return fmt.Errorf("create index: %w", err)
		}
	}
	if _, err := tx.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		return err
	}
	return tx.Commit()
}

func sessionsIDIsText(db *DB) bool {
	var typ string
	err := db.QueryRow(`SELECT type FROM pragma_table_info('sessions') WHERE name='id'`).Scan(&typ)
	return err == nil && typ == "TEXT"
}
