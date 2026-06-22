package authorship

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Working-log + baseline storage. The working log is the source of truth for
// uncommitted authorship; it is plain files under the repo's .git so the
// checkpoint→diff→commit path needs neither a daemon nor a database (G4).
//
// Layout (docs/attribution-v2-design.md §3.2):
//
//	.git/blamely/working_logs/<branch>/<base_sha>/<path>.json        ← attributions
//	.git/blamely/working_logs/<branch>/<base_sha>/.baselines/<path>  ← content the
//	                                                                    attributions describe
//
// Everything here is path/filepath-based and uses temp+rename, so it behaves
// identically on Windows, Linux, and macOS.

// workingLogDir is the per-(repo,branch,base_sha) directory. Rotating base_sha on
// commit gives a fresh tree for free; sanitizing the branch keeps slashes and
// Windows-illegal characters out of path components.
func workingLogDir(repoRoot, branch, baseSHA string) string {
	return filepath.Join(repoRoot, ".git", "blamely", "working_logs",
		sanitizeComponent(branch), sanitizeComponent(baseSHA))
}

// WorkingLogPath is where relPath's attributions live (mirrors the repo tree).
func WorkingLogPath(repoRoot, branch, baseSHA, relPath string) string {
	return filepath.Join(workingLogDir(repoRoot, branch, baseSHA),
		filepath.FromSlash(cleanRel(relPath))+".json")
}

// BaselinePath is where relPath's last-known content lives (raw, no extension).
func BaselinePath(repoRoot, branch, baseSHA, relPath string) string {
	return filepath.Join(workingLogDir(repoRoot, branch, baseSHA), ".baselines",
		filepath.FromSlash(cleanRel(relPath)))
}

// sanitizeComponent makes an arbitrary string safe as a SINGLE path component on
// all three OSes: branch names contain '/', and Windows forbids \ : * ? " < > |.
func sanitizeComponent(s string) string {
	if s == "" {
		return "_"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' ||
			r == '"' || r == '<' || r == '>' || r == '|' || r < 0x20:
			b.WriteByte('-')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// cleanRel normalizes a repo-relative path to forward slashes and strips any
// leading "./" or drive/leading separators, so it maps to a stable subtree.
func cleanRel(rel string) string {
	rel = filepath.ToSlash(rel)
	rel = strings.TrimPrefix(rel, "./")
	rel = strings.TrimLeft(rel, "/")
	return rel
}

// LoadWorkingLog reads relPath's working log, or (nil, nil) if none exists yet.
func LoadWorkingLog(repoRoot, branch, baseSHA, relPath string) (*WorkingLog, error) {
	return loadWorkingLogFile(WorkingLogPath(repoRoot, branch, baseSHA, relPath))
}

func loadWorkingLogFile(path string) (*WorkingLog, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var wl WorkingLog
	if err := json.Unmarshal(data, &wl); err != nil {
		return nil, fmt.Errorf("authorship: parse working log %s: %w", path, err)
	}
	return &wl, nil
}

func loadBaseline(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(data), true
}

// PutBaseline records relPath's current content as the pre-edit baseline (the
// `record --pre` fallback in Decision B). Subsequent Update calls diff against it
// when no working-log state exists yet.
func PutBaseline(repoRoot, branch, baseSHA, relPath, content string) error {
	return atomicWrite(BaselinePath(repoRoot, branch, baseSHA, relPath), []byte(content))
}

// SeedWorkingLog writes a STARTING working log + baseline for relPath at (branch,
// baseSHA) from a known per-line attribution — used to carry COMMITTED authorship
// across a commit so re-editing an unchanged committed line keeps its real author
// instead of defaulting to Human (I5). It is a no-op if a working log already
// exists, so it never clobbers uncommitted state. nowMS=0 leaves the stamp unset
// (the next Update restamps). Held under the same per-file lock as Update.
func SeedWorkingLog(repoRoot, branch, baseSHA, relPath, content string, lines []LineAttribution, nowMS int64) error {
	rel := cleanRel(relPath)
	wlPath := WorkingLogPath(repoRoot, branch, baseSHA, relPath)
	basePath := BaselinePath(repoRoot, branch, baseSHA, relPath)
	return withFileLock(wlPath, func() error {
		existing, err := loadWorkingLogFile(wlPath)
		if err != nil {
			return err
		}
		if existing != nil {
			return nil // already tracking this file — don't overwrite
		}
		wl := &WorkingLog{
			Schema: WorkingLogSchema, File: rel, BaseSHA: baseSHA,
			BlobSHA: sha256Hex(content), UpdatedMS: nowMS, Lines: lines,
		}
		data, err := json.MarshalIndent(wl, "", "  ")
		if err != nil {
			return err
		}
		if err := atomicWrite(wlPath, data); err != nil {
			return err
		}
		return atomicWrite(basePath, []byte(content))
	})
}

// Update applies one observed edit to relPath's working log under a per-file lock:
// it diffs the stored baseline (the content the current attributions describe)
// against newContent, attributes the changed lines to author, and persists both
// the updated log and newContent as the next baseline.
//
// fallbackBaseline is used as the diff's old side ONLY on the first observed edit
// (no stored baseline): pass the pre-edit capture or HEAD content per Decision B,
// or "" for a brand-new file. nowMS=0 stamps with the wall clock.
func Update(repoRoot, branch, baseSHA, relPath, newContent, fallbackBaseline string, author Author, nowMS int64) (*WorkingLog, error) {
	rel := cleanRel(relPath)
	wlPath := WorkingLogPath(repoRoot, branch, baseSHA, relPath)
	basePath := BaselinePath(repoRoot, branch, baseSHA, relPath)

	var result *WorkingLog
	err := withFileLock(wlPath, func() error {
		prior, err := loadWorkingLogFile(wlPath)
		if err != nil {
			return err
		}
		baseline := fallbackBaseline
		if stored, ok := loadBaseline(basePath); ok {
			baseline = stored
		}

		wl := Attribute(prior, baseline, newContent, author, nowMS)
		wl.File, wl.BaseSHA = rel, baseSHA

		data, err := json.MarshalIndent(wl, "", "  ")
		if err != nil {
			return err
		}
		if err := atomicWrite(wlPath, data); err != nil {
			return err
		}
		if err := atomicWrite(basePath, []byte(newContent)); err != nil {
			return err
		}
		// Record lines this edit removed (attributed to `author`) in the separate
		// deletions log, so the commit note can attribute deletions too.
		_ = AppendDeletions(repoRoot, branch, baseSHA, rel, DeletedBaselineLines(baseline, newContent), author)
		result = wl
		return nil
	})
	return result, err
}

// atomicWrite writes data to path via temp-file + rename (atomic, replace-existing
// on all three OSes — Go's os.Rename uses MOVEFILE_REPLACE_EXISTING on Windows).
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".wl-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // harmless no-op once the rename succeeds
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

const (
	lockTimeout = 5 * time.Second
	lockStale   = 10 * time.Second
	lockPoll    = 15 * time.Millisecond
)

// withFileLock serializes the read-modify-write of one working log across the two
// writers (editor plugin and CLI). It uses an O_CREATE|O_EXCL lock file rather
// than flock, since Go has no portable flock; a lock older than lockStale is
// treated as orphaned (writer crashed) and stolen.
func withFileLock(target string, fn func() error) error {
	lockPath := target + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return err
	}
	deadline := time.Now().Add(lockTimeout)
	for {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			f.Close()
			defer os.Remove(lockPath)
			return fn()
		}
		if !os.IsExist(err) {
			return err
		}
		if fi, e := os.Stat(lockPath); e == nil && time.Since(fi.ModTime()) > lockStale {
			os.Remove(lockPath) // orphaned lock from a crashed writer — steal it
			continue
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("authorship: timed out acquiring lock %s", lockPath)
		}
		time.Sleep(lockPoll)
	}
}

// AuthorTypesForFile loads relPath's working log and returns its per-line author
// types (1-based line number → "ai"/"human"), plus whether a log was found. Used
// by the Phase 2 dual-run comparison and, after the flip, by note generation.
func AuthorTypesForFile(repoRoot, branch, baseSHA, relPath string) (map[int]AuthorType, bool) {
	wl, err := LoadWorkingLog(repoRoot, branch, baseSHA, relPath)
	if err != nil || wl == nil {
		return nil, false
	}
	m := make(map[int]AuthorType, len(wl.Lines))
	for _, r := range wl.Lines {
		for ln := r.Start; ln <= r.End; ln++ {
			m[ln] = r.Author.Type
		}
	}
	return m, true
}

// AuthorsForFile is AuthorTypesForFile but returns the FULL author (tool, model,
// gen_type), used by the Phase 3 flip to rewrite a note's per-line attribution.
func AuthorsForFile(repoRoot, branch, baseSHA, relPath string) (map[int]Author, bool) {
	wl, err := LoadWorkingLog(repoRoot, branch, baseSHA, relPath)
	if err != nil || wl == nil {
		return nil, false
	}
	m := make(map[int]Author, len(wl.Lines))
	for _, r := range wl.Lines {
		for ln := r.Start; ln <= r.End; ln++ {
			m[ln] = r.Author
		}
	}
	return m, true
}

// ListWorkingLogs returns every file's working log under (repoRoot, branch, baseSHA)
// — the set of files with uncommitted v2 attribution this work cycle. Used to paint
// the gutter/sidebar repo-wide from one source. Missing dir → empty, not an error.
func ListWorkingLogs(repoRoot, branch, baseSHA string) ([]*WorkingLog, error) {
	dir := workingLogDir(repoRoot, branch, baseSHA)
	var out []*WorkingLog
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			// Skip the .baselines subtree (raw content, not logs).
			if d.Name() == ".baselines" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".json") {
			return nil
		}
		wl, lerr := loadWorkingLogFile(path)
		if lerr != nil || wl == nil {
			return nil // skip unreadable/partial entries; never fail the whole scan
		}
		out = append(out, wl)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return out, err
	}
	return out, nil
}

// MigrateWorkingLogs re-keys working logs (and their baselines) from oldBase to
// newBase under the same branch, updating each log's BaseSHA. Called post-commit to
// move uncommitted attribution onto the new HEAD so it survives the commit (a partial
// commit of one file must not strand another file's working log) and stays bounded —
// logs follow HEAD instead of accumulating at ancestor bases.
//
// Files in `committed` (the files this commit included) are DELETED from oldBase:
// their per-line attribution now lives durably in the git note (pushed to the
// remote) and in SQLite (from which amend/rebase re-attribution reconciles), so
// keeping a per-commit working log would only accumulate on disk without bound.
// A file whose newBase log already exists (a fresher editor flush) wins; the old
// one is dropped. No-op when oldBase == newBase or there is nothing to move.
func MigrateWorkingLogs(repoRoot, branch, oldBase, newBase string, committed map[string]bool) error {
	if oldBase == "" || newBase == "" || oldBase == newBase {
		return nil
	}
	logs, err := ListWorkingLogs(repoRoot, branch, oldBase)
	if err != nil || len(logs) == 0 {
		return err
	}
	for _, wl := range logs {
		rel := wl.File
		if committed[rel] {
			// Committed in this commit: drop the working log (note + SQLite hold the
			// attribution now) instead of stranding it at the parent base forever.
			_ = os.Remove(WorkingLogPath(repoRoot, branch, oldBase, rel))
			_ = os.Remove(BaselinePath(repoRoot, branch, oldBase, rel))
			continue
		}
		oldWL := WorkingLogPath(repoRoot, branch, oldBase, rel)
		oldBaseline := BaselinePath(repoRoot, branch, oldBase, rel)
		newWL := WorkingLogPath(repoRoot, branch, newBase, rel)
		if _, statErr := os.Stat(newWL); statErr == nil {
			// A fresher log already exists at the new base — drop the stale one.
			_ = os.Remove(oldWL)
			_ = os.Remove(oldBaseline)
			continue
		}
		wl.BaseSHA = newBase
		data, merr := json.MarshalIndent(wl, "", "  ")
		if merr != nil {
			continue
		}
		if atomicWrite(newWL, data) != nil {
			continue
		}
		if content, ok := loadBaseline(oldBaseline); ok {
			_ = atomicWrite(BaselinePath(repoRoot, branch, newBase, rel), []byte(content))
		}
		_ = os.Remove(oldWL)
		_ = os.Remove(oldBaseline)
	}
	// Drop the old base dir once empty (committed logs deleted, uncommitted moved
	// forward). A non-empty dir — e.g. an unmigrated log — is left intact (harmless).
	if remaining, rerr := ListWorkingLogs(repoRoot, branch, oldBase); rerr == nil && len(remaining) == 0 {
		_ = os.RemoveAll(workingLogDir(repoRoot, branch, oldBase))
	}
	return nil
}
