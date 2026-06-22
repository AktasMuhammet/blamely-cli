package authorship

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// Attribute is THE attribution engine (docs/attribution-v2-design.md §7). Given a
// file's prior working log + the baseline content those attributions describe, and
// the new content produced by `author`'s edit, it returns the updated working log.
//
// The rule, with no content-hash guessing:
//   - A new line that is UNCHANGED vs the baseline (matched by an LCS alignment)
//     keeps its prior author — so a human line an AI edit merely re-emitted stays
//     Human (I3), even if identical content appears elsewhere (I2).
//   - A new line that is added or changed is attributed to `author`.
//   - Lines with no prior coverage default to Human (I5).
//
// Add (empty baseline → all `author`), edit (diff), and delete (lines vanish) all
// fall out of the same alignment. Reflow (whitespace-only changes) is handled by
// the normalized comparison in alignLines (Phase 4): a reindented/reformatted line
// keeps its prior author. Move detection is still layered in later (§8); a moved
// line is currently treated as changed → `author`, which is safe (it's a real
// edit) and never mislabels an in-place human line.
//
// nowMS is injected (not time.Now()) so callers/tests are deterministic; pass 0
// to stamp with the wall clock.
func Attribute(prior *WorkingLog, baseline, newContent string, author Author, nowMS int64) *WorkingLog {
	oldLines := splitLines(baseline)
	newLines := splitLines(newContent)

	// matchedOld[i] = index into oldLines that new line i is unchanged from, or -1.
	matchedOld := alignLines(oldLines, newLines)

	perLine := make([]Author, len(newLines))
	for i := range newLines {
		if j := matchedOld[i]; j >= 0 {
			// Unchanged line: carry the prior author (old line j+1, 1-based).
			perLine[i] = priorAuthorOr(prior, j+1)
		} else {
			perLine[i] = author
		}
	}

	// overrode[i] records the author a CHANGED line replaced, when its type differs
	// from the new author (a human rewriting AI code, or vice-versa) — an audit
	// marker, not a change to who owns the line now.
	overrode := detectOverrode(prior, matchedOld, len(oldLines), author)

	if nowMS == 0 {
		nowMS = time.Now().UnixMilli()
	}
	file := ""
	base := ""
	if prior != nil {
		file, base = prior.File, prior.BaseSHA
	}
	return &WorkingLog{
		Schema:    WorkingLogSchema,
		File:      file,
		BaseSHA:   base,
		BlobSHA:   sha256Hex(newContent),
		UpdatedMS: nowMS,
		Lines:     coalesce(perLine, overrode),
	}
}

// detectOverrode finds replace pairs and, for each changed NEW line whose replaced
// OLD line had a different-type author, records that prior author. It walks the LCS
// alignment gap by gap (unmatched old lines [oldCursor,gapOldEnd) vs unmatched new
// lines [i,gapNewEnd)) and pairs them positionally — deterministic and identical
// across the Go, TS, and Kotlin ports (the golden vectors enforce it). Pure inserts
// (empty old side of the gap) and pure deletes never produce an override.
func detectOverrode(prior *WorkingLog, matchedOld []int, nOld int, author Author) []*Author {
	m := len(matchedOld)
	overrode := make([]*Author, m)
	oldCursor := 0
	i := 0
	for i < m {
		if matchedOld[i] >= 0 {
			oldCursor = matchedOld[i] + 1
			i++
			continue
		}
		gapNewEnd := i
		for gapNewEnd < m && matchedOld[gapNewEnd] < 0 {
			gapNewEnd++
		}
		gapOldEnd := nOld
		if gapNewEnd < m {
			gapOldEnd = matchedOld[gapNewEnd]
		}
		for k := 0; i+k < gapNewEnd && oldCursor+k < gapOldEnd; k++ {
			replaced := priorAuthorOr(prior, oldCursor+k+1)
			if replaced.Type != author.Type {
				p := replaced
				overrode[i+k] = &p
			}
		}
		oldCursor = gapOldEnd
		i = gapNewEnd
	}
	return overrode
}

// priorAuthorOr returns the prior author of 1-based old line n, defaulting to
// Human when there is no prior log or the line is uncovered (I5).
func priorAuthorOr(prior *WorkingLog, n int) Author {
	if prior == nil {
		return HumanAuthor()
	}
	return prior.authorAtLine(n)
}

// splitLines splits content into lines WITHOUT a trailing empty element from a
// final newline, and strips a trailing CR so CRLF (Windows) and LF compare equal.
// This keeps attribution stable across line-ending differences on the 3 OSes.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "\n")
	if n := len(parts); n > 0 && parts[n-1] == "" {
		parts = parts[:n-1] // drop the empty piece after a final '\n'
	}
	for i := range parts {
		parts[i] = strings.TrimSuffix(parts[i], "\r")
	}
	return parts
}

// alignLines returns, for each NEW line index, the index of the OLD line it is
// unchanged from (an LCS match), or -1 if it is added/changed. Standard LCS DP
// with backtrack; positional matching is what makes duplicate identical lines
// resolve correctly (the unchanged occurrence matches in place; an extra copy is
// reported as added) without any content-hash disambiguation.
//
// Lines are compared WHITESPACE-NORMALIZED (Phase 4 reflow): a line that changed
// only in indentation / trailing or collapsed whitespace counts as unchanged and
// keeps its prior author — reformatting is not authorship ("formatting
// non-substantial"). A genuine content change still mismatches → the editor.
func alignLines(oldLines, newLines []string) []int {
	n, m := len(oldLines), len(newLines)
	matched := make([]int, m)
	for i := range matched {
		matched[i] = -1
	}
	if n == 0 || m == 0 {
		return matched
	}
	oldN := normalizeLinesForMatch(oldLines)
	newN := normalizeLinesForMatch(newLines)

	// dp[i][j] = LCS length of oldN[i:] and newN[j:].
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if oldN[i] == newN[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	// Backtrack to recover the matched (unchanged) pairs.
	i, j := 0, 0
	for i < n && j < m {
		if oldN[i] == newN[j] {
			matched[j] = i
			i++
			j++
		} else if dp[i+1][j] >= dp[i][j+1] {
			i++
		} else {
			j++
		}
	}
	return matched
}

// normalizeLineForMatch collapses a line to its whitespace-insensitive form: trim
// ends + collapse internal whitespace runs to a single space. This MUST produce
// identical output in the Go, TypeScript, and Kotlin ports (the golden vectors
// enforce it) so the three agree on what counts as a reflow.
func normalizeLineForMatch(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func normalizeLinesForMatch(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = normalizeLineForMatch(l)
	}
	return out
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
