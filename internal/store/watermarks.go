package store

import (
	"database/sql"
	"fmt"
	"time"
)

// Watermark is a file-tailing watcher's resume point for one source (transcript
// or log file). ByteOffset is how far the watcher has consumed; Size and
// MtimeNanos are recorded so a later scan can detect rotation/truncation (the
// file shrank or was replaced) and re-prime instead of seeking past EOF. Extra
// is an opaque per-watcher JSON blob for any additional cursor state (e.g. the
// chat watcher's tegOffset / nextReqIdx). It survives daemon restarts.
type Watermark struct {
	ByteOffset int64
	Size       int64
	MtimeNanos int64
	Extra      string
}

// LoadWatermark returns the saved watermark for (watcher, source) and ok=false
// if none exists yet (the watcher then primes from the start, as before).
func (db *DB) LoadWatermark(watcher, source string) (Watermark, bool) {
	var w Watermark
	var extra sql.NullString
	err := db.QueryRow(
		`SELECT byte_offset, size, mtime_nanos, extra
		 FROM watcher_watermarks WHERE watcher = ? AND source = ?`,
		watcher, source,
	).Scan(&w.ByteOffset, &w.Size, &w.MtimeNanos, &extra)
	if err != nil {
		return Watermark{}, false
	}
	w.Extra = extra.String
	return w, true
}

// SaveWatermark upserts the resume point for (watcher, source). Call it after a
// successful scan; it's a single indexed write, cheap enough per scan.
func (db *DB) SaveWatermark(watcher, source string, w Watermark) error {
	_, err := db.Exec(
		`INSERT INTO watcher_watermarks
		   (watcher, source, byte_offset, size, mtime_nanos, extra, updated_nanos)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(watcher, source) DO UPDATE SET
		   byte_offset   = excluded.byte_offset,
		   size          = excluded.size,
		   mtime_nanos   = excluded.mtime_nanos,
		   extra         = excluded.extra,
		   updated_nanos = excluded.updated_nanos`,
		watcher, source, w.ByteOffset, w.Size, w.MtimeNanos, nullableNonEmpty(w.Extra), time.Now().UnixNano(),
	)
	if err != nil {
		return fmt.Errorf("save watermark %s/%s: %w", watcher, source, err)
	}
	return nil
}

// DeleteWatermark removes a single watermark (e.g. when its source file is gone).
func (db *DB) DeleteWatermark(watcher, source string) error {
	_, err := db.Exec(`DELETE FROM watcher_watermarks WHERE watcher = ? AND source = ?`, watcher, source)
	return err
}

// PruneWatermarks drops watermarks not updated since beforeNanos, so entries for
// long-gone files don't accumulate. Returns the number removed.
func (db *DB) PruneWatermarks(beforeNanos int64) (int64, error) {
	res, err := db.Exec(`DELETE FROM watcher_watermarks WHERE updated_nanos < ?`, beforeNanos)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
