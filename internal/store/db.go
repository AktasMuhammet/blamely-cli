package store

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"

	"github.com/blamely/blamely/internal/config"
)

type DB struct {
	*sql.DB
}

func Open() (*DB, error) {
	if _, err := config.EnsureBlamelyDir(); err != nil {
		return nil, err
	}
	path, err := config.DBPath()
	if err != nil {
		return nil, err
	}
	return OpenAt(path)
}

// OpenAt opens (or creates) a SQLite database at an explicit path.
// Use this in tests to open a database in t.TempDir() without redirecting HOME.
func OpenAt(path string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db := &DB{sqlDB}
	if err := db.migrate(); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return db, nil
}

// migrations is an ordered list of SQL statements.
// Each entry is applied exactly once (tracked by schema_version).
// Rules:
//   - CREATE TABLE / CREATE INDEX use IF NOT EXISTS → safe to be in the initial set.
//   - ALTER TABLE statements are NOT idempotent; they must be appended as new
//     entries and will only run on DBs that haven't seen them yet.
var migrations = []string{
	/* 0 */ `CREATE TABLE IF NOT EXISTS schema_version (
		version INTEGER PRIMARY KEY
	)`,
	/* 1 */ `CREATE TABLE IF NOT EXISTS edits (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		ts            INTEGER NOT NULL,
		repo_path     TEXT    NOT NULL,
		file_path     TEXT    NOT NULL,
		tool          TEXT    NOT NULL,
		confidence    TEXT    NOT NULL,
		model         TEXT,
		input_tokens         INTEGER,
		output_tokens        INTEGER,
		cache_read_tokens    INTEGER,
		cache_write_tokens   INTEGER,
		hash_before   TEXT,
		hash_after    TEXT,
		raw_meta      TEXT
	)`,
	/* 2 */ `CREATE INDEX IF NOT EXISTS edits_repo_file_ts ON edits(repo_path, file_path, ts)`,
	/* 3 */ `CREATE TABLE IF NOT EXISTS edit_lines (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		edit_id     INTEGER NOT NULL REFERENCES edits(id) ON DELETE CASCADE,
		start_line  INTEGER NOT NULL,
		end_line    INTEGER NOT NULL,
		content_sha TEXT
	)`,
	/* 4 */ `CREATE INDEX IF NOT EXISTS edit_lines_edit ON edit_lines(edit_id)`,
	/* 5 */ `CREATE TABLE IF NOT EXISTS commits (
		sha          TEXT PRIMARY KEY,
		repo_path    TEXT NOT NULL,
		ts           INTEGER NOT NULL,
		note_written INTEGER NOT NULL DEFAULT 0
	)`,
	// Migration 6: generation type — HOW the edit was produced.
	// Values: chat | cli | completion | unknown
	//   chat       — conversational AI (Claude Code session, Cursor Composer, Copilot Chat)
	//   cli        — command-line tool (Codex CLI, claude CLI)
	//   completion — inline/tab completion (Copilot Tab, Cursor Tab)
	//   unknown    — not yet classified
	/* 6 */ `ALTER TABLE edits ADD COLUMN gen_type TEXT NOT NULL DEFAULT 'unknown'`,
	// Migration 7: suggested_lines — the AI's original suggestion size, captured
	// from the watcher's view of the tool payload before any user editing or
	// partial acceptance. Compare to SUM(end_line - start_line + 1) across
	// edit_lines for that edit to compute the acceptance ratio.
	/* 7 */ `ALTER TABLE edits ADD COLUMN suggested_lines INTEGER NOT NULL DEFAULT 0`,
	// Migration 8: prompts — the user's chat prompts, keyed by session_id, so the
	// conversation survives transcript-file rotation/deletion and can be shown by
	// reports and editor gutters without re-reading the transcript on disk.
	// Populated at commit/attribute time from each session's transcript. `seq` is
	// the user-turn order within the session; (session_id, seq) is unique so a
	// re-run upserts rather than duplicates.
	/* 8 */ `CREATE TABLE IF NOT EXISTS prompts (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id  TEXT    NOT NULL,
		repo_path   TEXT,
		tool        TEXT,
		seq         INTEGER NOT NULL,
		text        TEXT    NOT NULL,
		ts          INTEGER NOT NULL
	)`,
	/* 9 */ `CREATE UNIQUE INDEX IF NOT EXISTS prompts_session_seq ON prompts(session_id, seq)`,
	// Migration 10-14: branch-based work sessions. A session is (repo_path, branch,
	// base_sha) where base_sha is the HEAD commit while uncommitted work accrues.
	// One open session per branch at a time; each commit advances HEAD and closes
	// that session (the next edit opens a new row). edits.session_id links an edit
	// edits.branch is denormalized so the live gutter is a single indexed lookup.
	/* 10 */ `CREATE TABLE IF NOT EXISTS sessions (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		repo_path   TEXT NOT NULL,
		branch      TEXT NOT NULL,
		base_sha    TEXT
	)`,
	/* 11 */ `CREATE UNIQUE INDEX IF NOT EXISTS sessions_repo_branch_base ON sessions(repo_path, branch, base_sha)`,
	/* 12 */ `ALTER TABLE edits ADD COLUMN session_id INTEGER`,
	/* 13 */ `ALTER TABLE edits ADD COLUMN branch TEXT`,
	/* 14 */ `CREATE INDEX IF NOT EXISTS edits_repo_branch ON edits(repo_path, branch)`,
	// Migration 15: work-session ids INTEGER → TEXT UUID (see migrateWorkSessionsUUID).
	/* 15 */ `SELECT 1`,
}

func (db *DB) migrate() error {
	// Bootstrap: schema_version may not exist on a fresh DB.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (
		version INTEGER PRIMARY KEY
	)`); err != nil {
		return fmt.Errorf("bootstrap schema_version: %w", err)
	}

	var applied int
	_ = db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&applied)

	for i, stmt := range migrations {
		if i <= applied-1 {
			continue // already applied in a previous run
		}
		if i == 15 {
			if err := db.migrateWorkSessionsUUID(); err != nil {
				return fmt.Errorf("migration %d: %w", i, err)
			}
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("migration %d: %w", i, err)
		}
	}

	if len(migrations) > applied {
		if _, err := db.Exec(
			`INSERT INTO schema_version(version) VALUES (?)
			 ON CONFLICT(version) DO NOTHING`,
			len(migrations),
		); err != nil {
			return fmt.Errorf("bump schema_version: %w", err)
		}
	}
	return nil
}
