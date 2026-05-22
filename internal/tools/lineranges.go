package tools

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// LineRange is a 1-based, inclusive line range together with the SHA of the
// new text. Used as an anchor for re-locating the range after later edits.
type LineRange struct {
	Start      int
	End        int
	ContentSHA string
}

// LocateNewString finds where `newString` lives in `filePath` and returns
// its post-edit line range. If newString is empty (pure deletion) or not
// found, returns (nil, nil). Multi-occurrence matches return the first one.
func LocateNewString(filePath, newString string) (*LineRange, error) {
	if newString == "" {
		return nil, nil
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filePath, err)
	}
	idx := bytes.Index(data, []byte(newString))
	if idx < 0 {
		return nil, nil
	}
	startLine := 1 + bytes.Count(data[:idx], []byte("\n"))
	end := idx + len(newString)
	// Trailing newline on newString means its last line is the one BEFORE the next newline.
	trimmed := strings.TrimRight(newString, "\n")
	endLine := startLine + strings.Count(trimmed, "\n")
	_ = end
	return &LineRange{
		Start:      startLine,
		End:        endLine,
		ContentSHA: sha256Hex([]byte(trimmed)),
	}, nil
}

// LineRangeForWholeFile returns 1..N for a full-file Write.
func LineRangeForWholeFile(filePath string) (*LineRange, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", filePath, err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<24)
	lines := 0
	var hasher = sha256.New()
	for sc.Scan() {
		lines++
		hasher.Write(sc.Bytes())
		hasher.Write([]byte{'\n'})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", filePath, err)
	}
	if lines == 0 {
		return nil, nil
	}
	return &LineRange{Start: 1, End: lines, ContentSHA: hex.EncodeToString(hasher.Sum(nil))}, nil
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// narrowToChangedLines diffs oldStr against newStr, finds the line ranges
// within newStr whose content didn't exist in oldStr, and translates those
// (1-based, relative to newStr) into absolute file-line ranges using the
// already-located range of newStr in the file (`full`).
//
// Use case: an AI Edit's new_string usually carries a few unchanged context
// lines around the actual change so the patch matches uniquely. We don't
// want those context lines credited to the AI — only the genuinely new
// lines. The returned `suggested` count is the total number of lines marked
// new (sum of the returned ranges' sizes). Falls back to `full` when the
// strings are identical (no-op edit) or the diff yields no changes.
func narrowToChangedLines(oldStr, newStr string, full LineRange) ([]LineRange, int64) {
	changed := AddedOrChangedRanges([]byte(oldStr), []byte(newStr))
	if len(changed) == 0 {
		return []LineRange{full}, int64(full.End - full.Start + 1)
	}
	offset := full.Start - 1
	var out []LineRange
	var suggested int64
	for _, r := range changed {
		abs := LineRange{Start: offset + r.Start, End: offset + r.End}
		if abs.End > full.End {
			abs.End = full.End
		}
		out = append(out, abs)
		suggested += int64(abs.End - abs.Start + 1)
	}
	return out, suggested
}

// CountAddedLines returns the number of net-new lines in newStr vs oldStr —
// i.e. the same metric narrowToChangedLines yields when an absolute file
// location is unavailable. Used by Edit/MultiEdit handlers to still credit
// the AI with `suggested_lines` even when LocateNewString fails (file not
// on disk yet, etc.).
func CountAddedLines(oldStr, newStr string) int64 {
	var n int64
	for _, r := range AddedOrChangedRanges([]byte(oldStr), []byte(newStr)) {
		n += int64(r.End - r.Start + 1)
	}
	return n
}

// AddedOrChangedRanges returns the 1-based line ranges in `newContent` that
// were NOT present (by exact content) in `oldContent`. Used by the human-edit
// watcher to detect lines the user typed/modified between two file snapshots.
//
// Semantics: any line in the new file whose content doesn't appear ANYWHERE
// in the old file is treated as "changed". Consecutive changed lines are
// collapsed into a single LineRange. Pure reorderings (same lines, different
// order) report no changes — that's fine for attribution: the content the
// human is interacting with was already there.
//
// Limitations:
//   - If the user duplicates an existing line, the duplicate is NOT flagged
//     (its content was already in the old set). Acceptable: attribution
//     wouldn't change anyway.
//   - The range covers a *contiguous run* of changed lines. Two separate edits
//     in the same file produce two ranges.
func AddedOrChangedRanges(oldContent, newContent []byte) []LineRange {
	oldLines := bytes.Split(oldContent, []byte{'\n'})
	newLines := bytes.Split(newContent, []byte{'\n'})
	// Drop a trailing empty element produced by a final newline.
	if n := len(oldLines); n > 0 && len(oldLines[n-1]) == 0 {
		oldLines = oldLines[:n-1]
	}
	if n := len(newLines); n > 0 && len(newLines[n-1]) == 0 {
		newLines = newLines[:n-1]
	}
	oldSet := make(map[string]struct{}, len(oldLines))
	for _, l := range oldLines {
		oldSet[string(l)] = struct{}{}
	}
	var out []LineRange
	curStart := 0
	for i, l := range newLines {
		lineNum := i + 1
		_, present := oldSet[string(l)]
		if !present {
			if curStart == 0 {
				curStart = lineNum
			}
			continue
		}
		if curStart != 0 {
			out = append(out, LineRange{Start: curStart, End: lineNum - 1})
			curStart = 0
		}
	}
	if curStart != 0 {
		out = append(out, LineRange{Start: curStart, End: len(newLines)})
	}
	return out
}
