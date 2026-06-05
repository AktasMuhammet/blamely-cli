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
// new (sum of the returned ranges' sizes). Falls back to crediting the whole
// located span when the strings share no new lines (no-op / reorder).
//
// Crucially the output is emitted ONE RANGE PER LINE, each non-blank line
// carrying its own content_sha (sha256 of the line text, sans trailing \r) —
// exactly like the Write path's perLineShaRangesFromContent. This is what makes
// attribution survive line drift: when the user later inserts a line inside the
// AI-edited block, every AI line is re-located by hashing the CURRENT text and
// matching the stored sha at its new position, while the user's freshly-typed
// line (no matching sha) is correctly human. A coarse multi-line range with no
// sha matches purely by position, so an inserted line lands inside the range and
// is mislabelled AI. The note still stores collapsed ranges (collapseToRanges),
// but the recorded edit — and thus attribution — is line-by-line.
func narrowToChangedLines(oldStr, newStr string, full LineRange) ([]LineRange, int64) {
	newLines := strings.Split(newStr, "\n")
	if n := len(newLines); n > 0 && newLines[n-1] == "" {
		newLines = newLines[:n-1] // drop trailing empty from a final newline
	}
	offset := full.Start - 1

	// emit expands a 1-based [relStart, relEnd] span (relative to newStr) into
	// per-line absolute ranges, each carrying the line's content_sha.
	emit := func(relStart, relEnd int) []LineRange {
		var rs []LineRange
		for rel := relStart; rel <= relEnd; rel++ {
			abs := offset + rel
			if abs > full.End {
				break
			}
			text := ""
			if rel-1 >= 0 && rel-1 < len(newLines) {
				text = strings.TrimRight(newLines[rel-1], "\r")
			}
			sha := ""
			if strings.TrimSpace(text) != "" {
				sha = sha256Hex([]byte(text))
			}
			rs = append(rs, LineRange{Start: abs, End: abs, ContentSHA: sha})
		}
		return rs
	}

	changed := AddedOrChangedRanges([]byte(oldStr), []byte(newStr))
	if len(changed) == 0 {
		// No genuinely-new lines vs old_string. Credit the whole located span,
		// still line-by-line so it can't be matched positionally.
		out := emit(1, full.End-offset)
		return out, int64(len(out))
	}

	var out []LineRange
	var suggested int64
	for _, r := range changed {
		rs := emit(r.Start, r.End)
		out = append(out, rs...)
		suggested += int64(len(rs))
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
// Semantics: each occurrence of a line in `oldContent` "covers" exactly one
// occurrence of the same content in `newContent` (left-to-right, greedy).
// Any new occurrence beyond the old count is treated as changed/added.
// Consecutive changed lines are collapsed into a single LineRange.
//
// This multiset approach correctly detects duplicate lines: if the old file
// has two closing braces and the new file has three, the extra brace is
// flagged. The old set-membership approach missed this and silently dropped
// human-typed lines whose content already existed elsewhere in the file.
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
	// Count how many times each line content appears in the old file.
	remaining := make(map[string]int, len(oldLines))
	for _, l := range oldLines {
		remaining[string(l)]++
	}
	var out []LineRange
	curStart := 0
	for i, l := range newLines {
		lineNum := i + 1
		key := string(l)
		if remaining[key] > 0 {
			// This occurrence is "covered" by an old-file occurrence.
			remaining[key]--
			if curStart != 0 {
				out = append(out, LineRange{Start: curStart, End: lineNum - 1})
				curStart = 0
			}
		} else {
			// Extra occurrence — this line is new or duplicated beyond old count.
			if curStart == 0 {
				curStart = lineNum
			}
		}
	}
	if curStart != 0 {
		out = append(out, LineRange{Start: curStart, End: len(newLines)})
	}
	return out
}
