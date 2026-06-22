package gitnotes

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"

	"github.com/blamely/blamely/internal/authorship"
)

// SeedCommittedWorkingLog writes a working log for relPath at (branch, baseSHA) that
// reflects COMMITTED per-line authorship: `git blame` finds the commit that last
// touched each line, and that commit's blamely note says whether the line was AI
// (and which tool/model/gen_type) or human. Seeding this before the first post-commit
// edit means unchanged committed lines keep their real author instead of defaulting
// to Human (I5). Registered as authorship.SeedHook (see internal/tools).
//
// Flag-gated; no-op when a log already exists or the file isn't present at baseSHA.
// Best-effort and cross-platform (git plumbing + path-agnostic string parsing).
func SeedCommittedWorkingLog(repoPath, branch, baseSHA, relPath string) error {
	if !authorship.Enabled() {
		return nil
	}
	if wl, err := authorship.LoadWorkingLog(repoPath, branch, baseSHA, relPath); err != nil || wl != nil {
		return err // already tracking (or read error) → don't seed
	}
	content, ok := showFileAt(repoPath, baseSHA, relPath)
	if !ok {
		return nil // not present at base (e.g. a brand-new file) → nothing to seed
	}
	authors := committedAuthorsByLine(repoPath, baseSHA, relPath)
	if authors == nil {
		return nil
	}
	return authorship.SeedWorkingLog(repoPath, branch, baseSHA, relPath, content, collapseAuthors(authors), 0)
}

// committedAuthorsByLine returns the committed author of each line of relPath at
// baseSHA, in order, via `git blame --porcelain` + the per-commit note.
func committedAuthorsByLine(repoPath, baseSHA, relPath string) []authorship.Author {
	out, err := exec.Command("git", "-C", repoPath, "blame", "--porcelain", baseSHA, "--", relPath).Output()
	if err != nil {
		return nil
	}
	noteCache := map[string]*Note{}
	var authors []authorship.Author
	var curSHA string
	var curOrigin int
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		if line[0] == '\t' { // the line's content; curSHA/curOrigin describe it
			authors = append(authors, committedAuthorAt(repoPath, curSHA, curOrigin, relPath, noteCache))
			curOrigin++
			continue
		}
		fields := strings.SplitN(line, " ", 4)
		if len(fields[0]) == 40 && isHex40(fields[0]) { // header: "<sha> <orig> <final> [count]"
			curSHA = fields[0]
			if len(fields) > 1 {
				curOrigin, _ = strconv.Atoi(fields[1])
			}
		}
	}
	return authors
}

// committedAuthorAt returns the author of originLine in relPath as recorded by
// sha's note (AI range → that tool; otherwise Human).
func committedAuthorAt(repoPath, sha string, originLine int, relPath string, cache map[string]*Note) authorship.Author {
	note, ok := cache[sha]
	if !ok {
		note = loadNoteForSeed(repoPath, sha)
		cache[sha] = note
	}
	if note != nil {
		for _, f := range note.Files {
			if f.Path != relPath {
				continue
			}
			for i := range f.Lines {
				r := &f.Lines[i]
				if r.Type != "add" || r.Tool == "" { // AI lines carry a tool
					continue
				}
				if originLine >= r.Start && originLine <= r.End {
					a := authorship.Author{Type: authorship.AI, Tool: r.Tool}
					if r.Model != nil {
						a.Model = *r.Model
					}
					if r.GenType != nil {
						a.GenType = *r.GenType
					}
					return a
				}
			}
		}
	}
	return authorship.HumanAuthor()
}

// collapseAuthors turns a per-line author slice into contiguous LineAttribution
// ranges (1-based), merging adjacent lines with the identical author.
func collapseAuthors(perLine []authorship.Author) []authorship.LineAttribution {
	var out []authorship.LineAttribution
	for i, a := range perLine {
		ln := i + 1
		if n := len(out); n > 0 && out[n-1].End == ln-1 && out[n-1].Author == a {
			out[n-1].End = ln
			continue
		}
		out = append(out, authorship.LineAttribution{Start: ln, End: ln, Author: a})
	}
	return out
}

func showFileAt(repoPath, sha, relPath string) (string, bool) {
	out, err := exec.Command("git", "-C", repoPath, "show", sha+":"+relPath).Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

func loadNoteForSeed(repoPath, sha string) *Note {
	out, err := exec.Command("git", "-C", repoPath, "notes", "--ref="+NotesRef, "show", sha).Output()
	if err != nil {
		return nil
	}
	var note Note
	if json.Unmarshal(out, &note) != nil {
		return nil
	}
	return &note
}

func isHex40(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// UncommittedAddedLines returns the post-image line numbers of relPath that differ
// from HEAD in the working tree (`git diff HEAD`), i.e. the current uncommitted
// CHANGES. The v2 gutter intersects working-log authorship with this set so it marks
// only changed lines (not every committed line). Empty/no diff → empty set.
func UncommittedAddedLines(repoPath, relPath string) map[int]bool {
	set := map[int]bool{}
	out, err := exec.Command("git", "-C", repoPath, "diff", "HEAD", "--unified=0", "--no-color", "--", relPath).Output()
	if err != nil {
		return set
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "@@") {
			continue
		}
		// @@ -a,b +c,d @@ — collect the new-side range c..c+d-1.
		plus := strings.Index(line, "+")
		if plus < 0 {
			continue
		}
		seg := line[plus+1:]
		if sp := strings.IndexByte(seg, ' '); sp >= 0 {
			seg = seg[:sp]
		}
		start, count := 0, 1
		if comma := strings.IndexByte(seg, ','); comma >= 0 {
			start, _ = strconv.Atoi(seg[:comma])
			count, _ = strconv.Atoi(seg[comma+1:])
		} else {
			start, _ = strconv.Atoi(seg)
		}
		for i := 0; i < count; i++ {
			set[start+i] = true
		}
	}
	return set
}
