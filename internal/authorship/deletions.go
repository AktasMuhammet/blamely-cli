package authorship

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
)

// Deletion attribution (Attribution). The per-file working log only describes
// SURVIVING content, so removed lines are recorded separately, here, in an
// append-only JSONL per (repo, branch, base_sha): who deleted which line content.
// A separate file (not the per-file <path>.json) means the editor plugins' whole-file
// log rewrites can't drop these records. At commit the note's deleted-line content is
// matched against this log to attribute deletions (see internal/gitnotes). Editor-
// originated deletions are not yet recorded here and fall back to the legacy engine.

// deletionRecord is one removed line and who removed it (on disk in the JSONL).
type deletionRecord struct {
	File    string `json:"file"`
	Content string `json:"content"`
	Author  Author `json:"author"`
}

func deletionsLogPath(repoRoot, branch, baseSHA string) string {
	return filepath.Join(workingLogDir(repoRoot, branch, baseSHA), ".deletions.jsonl")
}

// AppendDeletions records that `author` removed the given line contents from relPath.
// Best-effort, append-only (O_APPEND keeps concurrent per-file writers from clobbering
// each other). No-op for an empty list.
func AppendDeletions(repoRoot, branch, baseSHA, relPath string, contents []string, author Author) error {
	if len(contents) == 0 {
		return nil
	}
	path := deletionsLogPath(repoRoot, branch, baseSHA)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	rel := cleanRel(relPath)
	for _, c := range contents {
		line, merr := json.Marshal(deletionRecord{File: rel, Content: c, Author: author})
		if merr != nil {
			continue
		}
		if _, werr := f.Write(append(line, '\n')); werr != nil {
			return werr
		}
	}
	return nil
}

// LoadDeletions returns, for (repo, branch, base), a map of file → deleted-line
// content → author. Later records win (a line deleted, restored, re-deleted reflects
// the last deleter). Missing log → empty map, not an error.
func LoadDeletions(repoRoot, branch, baseSHA string) (map[string]map[string]Author, error) {
	out := make(map[string]map[string]Author)
	f, err := os.Open(deletionsLogPath(repoRoot, branch, baseSHA))
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		var r deletionRecord
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue
		}
		byContent := out[r.File]
		if byContent == nil {
			byContent = make(map[string]Author)
			out[r.File] = byContent
		}
		byContent[r.Content] = r.Author
	}
	return out, sc.Err()
}
