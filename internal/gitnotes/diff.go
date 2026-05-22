package gitnotes

import (
	"bufio"
	"fmt"
	"os/exec"
	"strings"
)

// AddedLine is one line that was added (or modified) by the commit.
// LineNum is the 1-based line number in the POST-commit file.
type AddedLine struct {
	File    string
	LineNum int
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
func DiffCommit(repoPath, sha string) (*CommitChange, error) {
	parent, hasParent, err := parentSHA(repoPath, sha)
	if err != nil {
		return nil, err
	}
	// -M  rename detection, -C  copy detection (so we can tag COPIED files).
	var args []string
	if hasParent {
		args = []string{"-C", repoPath, "diff", "--unified=0", "--no-color", "-M", "-C", parent + ".." + sha}
	} else {
		emptyTree, err := emptyTreeSHA(repoPath)
		if err != nil {
			return nil, err
		}
		args = []string{"-C", repoPath, "diff", "--unified=0", "--no-color", "-M", "-C", emptyTree, sha}
	}
	cmd := exec.Command("git", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("git diff: %w", err)
	}

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
	sc := bufio.NewScanner(stdout)
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
		switch {
		case strings.HasPrefix(line, "diff --git "):
			// "diff --git a/PRE b/POST" — extract POST as the file's identity.
			// We treat each new diff section as MODIFIED by default; later
			// headers (new file/deleted file/rename) override.
			diffHeaderPath = postPathFromDiffHeader(line)
			if diffHeaderPath != "" {
				if _, ok := out.FileChanges[diffHeaderPath]; !ok {
					out.FileChanges[diffHeaderPath] = FileModified
				}
			}
			pendingRenameFrom = ""
			pendingCopyFrom = ""
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
			delStart, addStart, ok := parseHunkHeaderBothSides(line)
			if !ok {
				continue
			}
			curLine = addStart
			curDelLine = delStart
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			if curFile != "" && curLine > 0 {
				// Skip whitespace-only added lines: blank lines, indentation-
				// only lines, etc. don't carry meaningful attribution and
				// would otherwise be misattributed to whichever tool last
				// wrote near that location.
				content := line[1:]
				if strings.TrimSpace(content) != "" {
					out.Added = append(out.Added, AddedLine{File: curFile, LineNum: curLine})
				}
				curLine++
			}
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			if curDelFile != "" && curDelLine > 0 {
				out.Deleted[curDelFile] = append(out.Deleted[curDelFile], curDelLine)
			}
			curDelLine++
		case strings.HasPrefix(line, " "):
			curLine++
			curDelLine++
		}
	}
	_ = cmd.Wait()
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
