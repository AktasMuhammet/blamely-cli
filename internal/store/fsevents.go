package store

import "fmt"

// File lifecycle events reported by the editor plugins (VS Code / IntelliJ) so
// that attribution rows in `edits` track the file as it is deleted, restored,
// renamed/moved, or copied. All paths are repo-relative with forward slashes —
// the same form the plugins use for `file_path` when recording an edit — so the
// repo_path + file_path match is exact.
//
// Delete is SOFT: we stamp deleted_at instead of removing rows, so an undo or
// branch rollback that re-creates the file recovers the history via RestoreFile.

// MarkFileDeleted soft-deletes every live edit row for repo/file by stamping
// deleted_at = atNanos. Rows already deleted are left untouched (keeps the
// original deletion timestamp). Returns the number of rows newly marked.
func (db *DB) MarkFileDeleted(repoPath, filePath string, atNanos int64) (int64, error) {
	res, err := db.Exec(
		`UPDATE edits SET deleted_at = ?
		 WHERE repo_path = ? AND file_path = ? AND deleted_at IS NULL`,
		atNanos, repoPath, filePath,
	)
	if err != nil {
		return 0, fmt.Errorf("mark file deleted: %w", err)
	}
	return res.RowsAffected()
}

// RestoreFile clears the soft-delete flag for repo/file (undo a deletion, or a
// file re-created at the same path). Returns the number of rows restored.
func (db *DB) RestoreFile(repoPath, filePath string) (int64, error) {
	res, err := db.Exec(
		`UPDATE edits SET deleted_at = NULL
		 WHERE repo_path = ? AND file_path = ? AND deleted_at IS NOT NULL`,
		repoPath, filePath,
	)
	if err != nil {
		return 0, fmt.Errorf("restore file: %w", err)
	}
	return res.RowsAffected()
}

// MoveFileAttribution re-points every edit row for repo/oldPath at newPath
// (rename or move). It also clears any soft-delete on those rows, since the
// file demonstrably exists at the destination. No-op when old and new match.
// Returns the number of rows moved.
func (db *DB) MoveFileAttribution(repoPath, oldPath, newPath string) (int64, error) {
	if oldPath == newPath {
		return 0, nil
	}
	res, err := db.Exec(
		`UPDATE edits SET file_path = ?, deleted_at = NULL
		 WHERE repo_path = ? AND file_path = ?`,
		newPath, repoPath, oldPath,
	)
	if err != nil {
		return 0, fmt.Errorf("move file attribution: %w", err)
	}
	return res.RowsAffected()
}

// CopyFileAttribution clones the LIVE edit rows (and their line ranges) of
// repo/srcPath onto dstPath so a copied file keeps the same AI/human
// attribution. Soft-deleted source rows are not copied. Existing live rows on
// dstPath are removed first so a re-copy is idempotent rather than additive.
// Returns the number of edit rows cloned. No-op when src and dst match.
func (db *DB) CopyFileAttribution(repoPath, srcPath, dstPath string) (int, error) {
	if srcPath == dstPath {
		return 0, nil
	}
	src, err := db.editsForFileWhere(
		`repo_path = ? AND file_path = ? AND deleted_at IS NULL`,
		repoPath, srcPath,
	)
	if err != nil {
		return 0, fmt.Errorf("copy: load source edits: %w", err)
	}
	if len(src) == 0 {
		return 0, nil
	}
	// Drop any prior clones at the destination so repeated copies don't stack.
	if _, err := db.Exec(
		`DELETE FROM edits WHERE repo_path = ? AND file_path = ?`,
		repoPath, dstPath,
	); err != nil {
		return 0, fmt.Errorf("copy: clear destination: %w", err)
	}
	for i := range src {
		e := src[i]
		e.ID = 0
		e.FilePath = dstPath
		if _, err := db.InsertEdit(e); err != nil {
			return i, fmt.Errorf("copy: insert clone: %w", err)
		}
	}
	return len(src), nil
}
