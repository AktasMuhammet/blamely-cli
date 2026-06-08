package report

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/blamely/blamely/internal/gitnotes"
	"github.com/blamely/blamely/internal/gitutil"
)

// RenderBlame prints a per-line attribution view of a file at the given
// revision (default HEAD): for every line it shows the commit that introduced
// it plus who wrote it — a human author's name, or "<tool> · <model>" when
// blamely's git notes show the line was AI-generated — followed by the line
// number and the source text. It's `git blame` with the AI-vs-human story
// layered on top, in the same premium visual language as the rest of report.
func RenderBlame(file, rev string) error {
	repo, ok := gitutil.Toplevel(".")
	if !ok {
		return fmt.Errorf("not inside a git repository")
	}
	if rev == "" {
		rev = "HEAD"
	}

	relPath, err := repoRelativePath(repo, file)
	if err != nil {
		return err
	}

	entries, err := gitBlame(repo, rev, relPath)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Fprintf(os.Stdout, "%s\n", dim("(empty file)"))
		return nil
	}

	notes := map[string]*gitnotes.Note{} // commit sha -> parsed note (nil = no note)
	w := os.Stdout

	shaW, authorW, lineW := 7, 0, len(strconv.Itoa(entries[len(entries)-1].finalLine))
	for _, e := range entries {
		if l := len(attributionLabel(repo, e, notes, relPath)); l > authorW {
			authorW = l
		}
	}

	fmt.Fprintf(w, "\n%s  %s\n\n", bold(relPath), dim("@ "+rev))
	for _, e := range entries {
		sha := e.sha
		if len(sha) > shaW {
			sha = sha[:shaW]
		}
		label := attributionLabel(repo, e, notes, relPath)
		coloredLabel := colorAttributionLabel(repo, e, notes, relPath, label)
		pad := strings.Repeat(" ", authorW-len(label))
		fmt.Fprintf(w, "  %s  %s%s  %s  %s %s\n",
			dim(sha),
			coloredLabel, pad,
			dim(fmt.Sprintf("%*d", lineW, e.finalLine)),
			dim("│"),
			e.content)
	}
	fmt.Fprintln(w)
	return nil
}

// repoRelativePath resolves a user-supplied file argument (relative to cwd or
// already repo-relative) to a path relative to the repo root, as required by
// `git blame`.
func repoRelativePath(repo, file string) (string, error) {
	if filepath.IsAbs(file) {
		rel, err := filepath.Rel(repo, file)
		if err != nil {
			return "", fmt.Errorf("resolve %s relative to repo: %w", file, err)
		}
		return filepath.ToSlash(rel), nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	abs := filepath.Join(cwd, file)
	rel, err := filepath.Rel(repo, abs)
	if err != nil {
		return "", fmt.Errorf("resolve %s relative to repo: %w", file, err)
	}
	return filepath.ToSlash(rel), nil
}

// blameEntry is one source line as reported by `git blame --porcelain`,
// carrying enough commit metadata to render the gutter and look up notes.
type blameEntry struct {
	sha         string
	authorName  string
	originLine  int
	finalLine   int
	content     string
}

// gitBlame runs `git blame --porcelain` for path at rev and parses its output
// into one blameEntry per final-image line. The porcelain format interleaves
// per-commit header blocks (emitted once per commit, the first time it's seen)
// with per-line "<tab>content" records — see `git help blame`.
func gitBlame(repo, rev, path string) ([]blameEntry, error) {
	cmd := exec.Command("git", "-C", repo, "blame", "--porcelain", rev, "--", path)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git blame %s: %w", path, err)
	}

	type commitMeta struct {
		authorName string
	}
	commits := map[string]*commitMeta{}

	var entries []blameEntry
	var cur *commitMeta
	var curSHA string
	var curOrigin, curFinal, curCount int

	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		if line[0] == '\t' {
			entries = append(entries, blameEntry{
				sha:        curSHA,
				authorName: cur.authorName,
				originLine: curOrigin,
				finalLine:  curFinal,
				content:    line[1:],
			})
			curOrigin++
			curFinal++
			continue
		}
		fields := strings.SplitN(line, " ", 4)
		// A header line is "<sha40> <origin-line> <final-line>[ <count>]".
		if len(fields[0]) == 40 && isHex(fields[0]) {
			curSHA = fields[0]
			if len(fields) > 1 {
				curOrigin, _ = strconv.Atoi(fields[1])
			}
			if len(fields) > 2 {
				curFinal, _ = strconv.Atoi(fields[2])
			}
			if len(fields) > 3 {
				curCount, _ = strconv.Atoi(fields[3])
				_ = curCount
			}
			meta, ok := commits[curSHA]
			if !ok {
				meta = &commitMeta{}
				commits[curSHA] = meta
			}
			cur = meta
			continue
		}
		switch {
		case strings.HasPrefix(line, "author "):
			cur.authorName = strings.TrimPrefix(line, "author ")
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// attributionLabel returns the plain-text label for a blamed line: the AI
// "<tool> · <model>" when blamely's note for e.sha shows this line was
// AI-generated, otherwise the commit author's name.
func attributionLabel(repo string, e blameEntry, cache map[string]*gitnotes.Note, path string) string {
	if r := aiRangeFor(repo, e, cache, path); r != nil {
		label := r.Tool
		if r.Model != nil && *r.Model != "" {
			label += " · " + *r.Model
		}
		return label
	}
	return e.authorName
}

// colorAttributionLabel renders the label in green for AI-attributed lines or
// blue for human ones, matching the AI/Human color language used elsewhere
// in report (RenderBar, formatAttribution).
func colorAttributionLabel(repo string, e blameEntry, cache map[string]*gitnotes.Note, path, label string) string {
	if aiRangeFor(repo, e, cache, path) != nil {
		return green(label)
	}
	return blue(label)
}

// aiRangeFor looks up the blamely note for e.sha (caching results, including
// misses, in cache) and returns the RangeEntry covering e.originLine in path,
// if that range is AI-attributed. Returns nil for human lines, deletions, or
// commits with no note.
func aiRangeFor(repo string, e blameEntry, cache map[string]*gitnotes.Note, path string) *gitnotes.RangeEntry {
	note, ok := cache[e.sha]
	if !ok {
		note = loadNote(repo, e.sha)
		cache[e.sha] = note
	}
	if note == nil {
		return nil
	}
	for _, f := range note.Files {
		if f.Path != path {
			continue
		}
		for i := range f.Lines {
			r := &f.Lines[i]
			if r.Type != "add" || r.Tool == "" {
				continue
			}
			if e.originLine >= r.Start && e.originLine <= r.End {
				return r
			}
		}
	}
	return nil
}

// loadNote reads and parses the blamely git note for sha, or nil if there is
// none (or it fails to parse) — a miss just means "treat lines as human".
func loadNote(repo, sha string) *gitnotes.Note {
	out, err := exec.Command("git", "-C", repo, "notes", "--ref="+gitnotes.NotesRef, "show", sha).Output()
	if err != nil {
		return nil
	}
	var note gitnotes.Note
	if err := json.Unmarshal(out, &note); err != nil {
		return nil
	}
	return &note
}
