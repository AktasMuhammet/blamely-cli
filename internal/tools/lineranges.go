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
