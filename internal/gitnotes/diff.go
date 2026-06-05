package gitnotes

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/blamely/blamely/internal/config"
)

// AddedLine is one line that was added (or modified) by the commit.
// LineNum is the 1-based line number in the POST-commit file.
// Content is the raw text of the line (without the leading '+'), stripped of
// trailing carriage returns. Used as a ContentSHA fallback in attribution so
// that Copilot/chat edits survive line-number drift caused by human edits made
// to the file after the AI applied them but before the commit.
type AddedLine struct {
	File    string
	LineNum int
	Content string
}

// FileChangeType is the file-level change kind: ADDED, DELETED, MODIFIED,
// RENAMED, or COPIED. Surfaced in the git note so consumers can render a
// per-file status without re-running `git diff`. Move is a special case of
// renamed (same name, different directory) — git doesn't expose it
// separately, so we report it as RENAMED.
type FileChangeType string

const (
	FileAdded    FileChangeType = "ADDED"
	FileDeleted  FileChangeType = "DELETED"
	FileModified FileChangeType = "MODIFIED"
	FileRenamed  FileChangeType = "RENAMED"
	FileCopied   FileChangeType = "COPIED"
)

// CommitChange is the diff result of a single commit: every added line, the
// per-file pre-image line numbers that were deleted, a post→pre rename map
// so the attribute step can look up edits recorded under the old filename,
// the file-level change kind keyed by post-commit path, and an analogous
// copy map for files git detected as copy-with-modifications.
type CommitChange struct {
	Added       []AddedLine
	Deleted     map[string][]int          // pre-commit path → deleted line numbers (pre-image)
	Renames     map[string]string         // post-commit path → pre-commit path (renamed)
	Copies      map[string]string         // post-commit path → pre-commit path (copied)
	FileChanges map[string]FileChangeType // post-commit path → file-level change kind
}

// DeletedCount returns the per-file count derived from Deleted line numbers.
// Used by call sites that historically consumed map[string]int.
func (c *CommitChange) DeletedCount(file string) int {
	return len(c.Deleted[file])
}

// ChangedLines returns every added/modified line of `sha` in `repoPath`,
// using the post-commit line numbers.
func ChangedLines(repoPath, sha string) ([]AddedLine, error) {
	c, err := DiffCommit(repoPath, sha)
	if err != nil {
		return nil, err
	}
	return c.Added, nil
}

// DiffCommit is ChangedLines + rename tracking. Uses `git diff -M` so
// renames are surfaced as `rename from` / `rename to` headers instead of
// add+delete pairs.
//
// Files matching ~/.blamely/exclude or the repo's .gitignore are filtered
// out at parse time — they never appear in CommitChange and therefore
// never enter attribution or reports.
func DiffCommit(repoPath, sha string) (*CommitChange, error) {
	parent, hasParent, err := parentSHA(repoPath, sha)
	if err != nil {
		return nil, err
	}
	// -M rename detection only. We deliberately do NOT pass -C (copy detection):
	// when an AI Writes a brand-new file that happens to resemble an existing one
	// (e.g. contact.html created from register.html), -C makes git report it as a
	// COPY and emit only the handful of differing lines as "added" — so a 146-line
	// AI-authored file shows added=43 and the rest of the AI's work vanishes from
	// the note. Without -C the file is a normal new file with all 146 lines added,
	// matching `git show --stat` and attributing every line the AI wrote.
	// The first argument "-C repoPath" is git's own -C (change-directory) flag and
	// is unrelated to copy detection.
	var args []string
	if hasParent {
		args = []string{"-C", repoPath, "diff", "--unified=0", "--no-color", "-M", parent + ".." + sha}
	} else {
		emptyTree, err := emptyTreeSHA(repoPath)
		if err != nil {
			return nil, err
		}
		args = []string{"-C", repoPath, "diff", "--unified=0", "--no-color", "-M", emptyTree, sha}
	}
	cmd := exec.Command("git", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("git diff: %w", err)
	}
	// Lenient exclude-list load: any error (file missing, permission denied)
	// yields a nil filter, which the parser treats as "exclude nothing". A
	// broken exclude config must never break attribution.
	excl, _ := config.LoadExcludeListForRepo(repoPath)
	out, perr := parseDiff(stdout, excl)
	_ = cmd.Wait()
	if perr != nil {
		return nil, perr
	}
	return out, nil
}

// parseDiff reads `git diff --unified=0 -M -C` output and produces a
// CommitChange. Extracted from DiffCommit so the hunk-pairing logic can be
// exercised with hand-crafted diff strings in tests.
//
// Pairing semantics: within each hunk we buffer the `-` and `+` lines, then
// pair them positionally. The first min(len(-), len(+)) of each are treated
// as MODIFICATIONS — the `-` side is discarded (it would otherwise be
// reported as a "deletion treated as human" even though the change is an
// in-place replacement) and the `+` side is emitted as an added line subject
// to the whitespace filter. Excess `-` lines are pure deletes; excess `+`
// lines are pure adds. This is the only correct way to recognize modifications
// when an earlier hunk in the same file has shifted line numbers — comparing
// pre-image delete line numbers against post-image add line numbers
// produces false negatives the moment the file's net line count drifts.
func parseDiff(r io.Reader, excl *config.ExcludeList) (*CommitChange, error) {
	out := &CommitChange{
		Deleted:     map[string][]int{},
		Renames:     map[string]string{},
		Copies:      map[string]string{},
		FileChanges: map[string]FileChangeType{},
	}
	curFile := ""    // post-commit path (from +++ b/...)
	curDelFile := "" // pre-commit path (from --- a/...)
	curLine := 0     // post-image line number for the next + or context line
	curDelLine := 0  // pre-image line number for the next - or context line
	// diffHeaderPath is the post-commit path inferred from `diff --git a/X b/Y`.
	// We use it as the FileChanges key for new/deleted/renamed/copied files
	// before the +++/--- lines arrive (or when one of them is /dev/null).
	diffHeaderPath := ""
	pendingRenameFrom := ""
	pendingCopyFrom := ""
	// skipFile is set when the current file matched an exclude rule. While
	// true, every subsequent line (hunk header, +/-, file-mode lines) is
	// ignored until the next `diff --git` header resets the flag. Excluded
	// files leave no trace in CommitChange.
	skipFile := false

	// Per-hunk buffers — see function comment for pairing semantics.
	type hunkAdd struct {
		line    int
		content string
	}
	var hunkDels []int
	var hunkAdds []hunkAdd

	flushHunk := func() {
		n := len(hunkDels)
		if len(hunkAdds) < n {
			n = len(hunkAdds)
		}
		// Paired adds (modifications) — emit add side, drop delete side.
		// All added lines are counted, including blank/whitespace-only ones:
		// a blank line the author wrote is still a line they authored, and
		// counting it keeps the note's totals consistent with `git show --stat`.
		for i := 0; i < n; i++ {
			a := hunkAdds[i]
			if curFile != "" {
				out.Added = append(out.Added, AddedLine{File: curFile, LineNum: a.line, Content: strings.TrimRight(a.content, "\r")})
			}
		}
		// Excess deletes — lines removed without a same-position replacement.
		for i := n; i < len(hunkDels); i++ {
			if curDelFile != "" {
				out.Deleted[curDelFile] = append(out.Deleted[curDelFile], hunkDels[i])
			}
		}
		// Excess adds — lines added without a same-position predecessor.
		for i := n; i < len(hunkAdds); i++ {
			a := hunkAdds[i]
			if curFile != "" {
				out.Added = append(out.Added, AddedLine{File: curFile, LineNum: a.line, Content: strings.TrimRight(a.content, "\r")})
			}
		}
		hunkDels = hunkDels[:0]
		hunkAdds = hunkAdds[:0]
	}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<22)
	setFileChange := func(path string, kind FileChangeType) {
		if path == "" {
			return
		}
		// Don't downgrade a more-specific kind (ADDED/RENAMED) to MODIFIED.
		if existing, ok := out.FileChanges[path]; ok && existing != FileModified {
			return
		}
		out.FileChanges[path] = kind
	}
	for sc.Scan() {
		line := sc.Text()
		// File boundary: handle the `diff --git` header BEFORE the skip
		// gate so an excluded file can't carry skipFile into the next file.
		if strings.HasPrefix(line, "diff --git ") {
			// Flush the previous file's last hunk before its curFile /
			// curDelFile get overwritten.
			flushHunk()
			diffHeaderPath = postPathFromDiffHeader(line)
			pendingRenameFrom = ""
			pendingCopyFrom = ""
			// Reset the per-file skip flag and re-evaluate against the new
			// path. Excluded files leave no trace: no FileChanges entry,
			// no Added, no Deleted.
			skipFile = excl.Match(diffHeaderPath)
			if skipFile {
				// Also clear the working state so any trailing tokens from
				// the previous file (e.g. an unflushed +/- buffer slot)
				// don't get attributed to this excluded file.
				curFile = ""
				curDelFile = ""
				curLine = 0
				curDelLine = 0
				continue
			}
			if diffHeaderPath != "" {
				if _, ok := out.FileChanges[diffHeaderPath]; !ok {
					out.FileChanges[diffHeaderPath] = FileModified
				}
			}
			continue
		}
		if skipFile {
			continue
		}
		switch {
		case strings.HasPrefix(line, "new file mode"):
			setFileChange(diffHeaderPath, FileAdded)
		case strings.HasPrefix(line, "deleted file mode"):
			// Deleted files have no post-commit path; key by pre-commit path
			// which `diff --git a/X b/X` repeats on both sides.
			setFileChange(diffHeaderPath, FileDeleted)
		case strings.HasPrefix(line, "rename from "):
			pendingRenameFrom = strings.TrimPrefix(line, "rename from ")
		case strings.HasPrefix(line, "rename to "):
			to := strings.TrimPrefix(line, "rename to ")
			if pendingRenameFrom != "" {
				out.Renames[to] = pendingRenameFrom
				pendingRenameFrom = ""
			}
			setFileChange(to, FileRenamed)
		case strings.HasPrefix(line, "copy from "):
			pendingCopyFrom = strings.TrimPrefix(line, "copy from ")
		case strings.HasPrefix(line, "copy to "):
			to := strings.TrimPrefix(line, "copy to ")
			if pendingCopyFrom != "" {
				out.Copies[to] = pendingCopyFrom
				pendingCopyFrom = ""
			}
			setFileChange(to, FileCopied)
		case strings.HasPrefix(line, "--- a/"):
			curDelFile = strings.TrimPrefix(line, "--- a/")
		case strings.HasPrefix(line, "--- /dev/null"):
			curDelFile = "" // new file — no pre-commit path
		case strings.HasPrefix(line, "+++ b/"):
			curFile = strings.TrimPrefix(line, "+++ b/")
		case strings.HasPrefix(line, "+++ /dev/null"):
			curFile = "" // deleted file — no post-commit path
		case strings.HasPrefix(line, "@@"):
			// New hunk: flush the previous hunk's buffered lines before
			// resetting line counters.
			flushHunk()
			delStart, addStart, ok := parseHunkHeaderBothSides(line)
			if !ok {
				continue
			}
			curLine = addStart
			curDelLine = delStart
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			if curLine > 0 {
				hunkAdds = append(hunkAdds, hunkAdd{line: curLine, content: line[1:]})
				curLine++
			}
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			if curDelLine > 0 {
				hunkDels = append(hunkDels, curDelLine)
			}
			curDelLine++
		case strings.HasPrefix(line, " "):
			curLine++
			curDelLine++
		}
	}
	flushHunk()
	return out, sc.Err()
}

// postPathFromDiffHeader extracts POST from "diff --git a/PRE b/POST". POST
// may equal PRE for a simple modification; for renames the two differ and
// the dedicated `rename from`/`rename to` headers carry authoritative info.
// Returns "" on a malformed line.
func postPathFromDiffHeader(s string) string {
	const prefix = "diff --git "
	if !strings.HasPrefix(s, prefix) {
		return ""
	}
	rest := s[len(prefix):]
	i := strings.Index(rest, " b/")
	if i < 0 {
		return ""
	}
	return rest[i+len(" b/"):]
}

// parseHunkHeader extracts the "+start,len" from "@@ -a,b +start,len @@ ..."
// Length may be omitted (defaults to 1).
func parseHunkHeader(s string) (start, length int, ok bool) {
	// "@@ -1,2 +3,4 @@"
	const plus = " +"
	i := strings.Index(s, plus)
	if i < 0 {
		return 0, 0, false
	}
	rest := s[i+2:]
	j := strings.IndexAny(rest, " @")
	if j < 0 {
		return 0, 0, false
	}
	spec := rest[:j]
	if k := strings.IndexByte(spec, ','); k >= 0 {
		if _, err := fmt.Sscanf(spec, "%d,%d", &start, &length); err != nil {
			return 0, 0, false
		}
	} else {
		length = 1
		if _, err := fmt.Sscanf(spec, "%d", &start); err != nil {
			return 0, 0, false
		}
	}
	return start, length, true
}

// parseHunkHeaderBothSides returns both the pre-image and post-image starting
// line numbers from a "@@ -a,b +c,d @@" header. Either side's length may be
// omitted (defaults to 1). Returns ok=false if either side fails to parse.
func parseHunkHeaderBothSides(s string) (delStart, addStart int, ok bool) {
	const minus = "@@ -"
	i := strings.Index(s, minus)
	if i < 0 {
		return 0, 0, false
	}
	rest := s[i+len(minus):]
	j := strings.IndexByte(rest, ' ')
	if j < 0 {
		return 0, 0, false
	}
	delSpec := rest[:j]
	if k := strings.IndexByte(delSpec, ','); k >= 0 {
		if _, err := fmt.Sscanf(delSpec, "%d,%d", &delStart, new(int)); err != nil {
			return 0, 0, false
		}
	} else {
		if _, err := fmt.Sscanf(delSpec, "%d", &delStart); err != nil {
			return 0, 0, false
		}
	}
	add, _, addOK := parseHunkHeader(s)
	if !addOK {
		return 0, 0, false
	}
	return delStart, add, true
}

func parentSHA(repoPath, sha string) (string, bool, error) {
	out, err := exec.Command("git", "-C", repoPath, "rev-parse", sha+"^").Output()
	if err != nil {
		// First commit has no parent.
		return "", false, nil
	}
	return strings.TrimSpace(string(out)), true, nil
}

func emptyTreeSHA(repoPath string) (string, error) {
	out, err := exec.Command("git", "-C", repoPath, "hash-object", "-t", "tree", "/dev/null").Output()
	if err != nil {
		// Fallback to the well-known empty tree SHA-1 hash.
		return "4b825dc642cb6eb9a060e54bf8d69288fbee4904", nil
	}
	return strings.TrimSpace(string(out)), nil
}

// CommitMessage returns the full commit message body (subject + body) of sha.
// Empty string on error so callers can tolerate detached/missing state.
func CommitMessage(repoPath, sha string) string {
	out, err := exec.Command("git", "-C", repoPath, "show", "-s", "--format=%B", sha).Output()
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(out), "\n")
}

// BranchName returns the currently checked-out branch name of repoPath. On
// detached HEAD or any failure it returns "" so the note can still be
// produced. This is called immediately after the commit, while HEAD still
// points to the new commit on the recording branch.
func BranchName(repoPath string) string {
	out, err := exec.Command("git", "-C", repoPath, "branch", "--show-current").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func CommitTimestampNanos(repoPath, sha string) (int64, error) {
	out, err := exec.Command("git", "-C", repoPath, "show", "-s", "--format=%ct", sha).Output()
	if err != nil {
		return 0, fmt.Errorf("git show: %w", err)
	}
	var secs int64
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &secs); err != nil {
		return 0, fmt.Errorf("parse commit time: %w", err)
	}
	return secs * 1e9, nil
}
