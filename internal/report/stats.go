package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/blamely/blamely/internal/gitnotes"
	"github.com/blamely/blamely/internal/gitutil"
	"github.com/blamely/blamely/internal/store"
)

// RenderStats prints a deep single-commit view to stdout.
// It reads the blamely git note for sha and combines it with commit metadata.
func RenderStats(sha string) error {
	repoPath, ok := gitutil.Toplevel(".")
	if !ok {
		return fmt.Errorf("not inside a git repository")
	}
	db, err := store.Open()
	if err != nil {
		return err
	}
	defer db.Close()

	repoID, _ := gitutil.RepoID(repoPath)
	if repoID == "" {
		repoID = repoPath
	}

	noteBytes, err := readNote(repoPath, sha)
	if err != nil {
		return fmt.Errorf("no blamely note for %s: run `blamely attribute %s %s` first", sha, repoPath, sha)
	}
	var note gitnotes.Note
	if err := json.Unmarshal(noteBytes, &note); err != nil {
		return fmt.Errorf("parse note: %w", err)
	}

	// Commit metadata
	meta, err := commitMeta(repoPath, sha)
	if err != nil {
		meta = commitMeta_{"sha": sha}
	}

	// Session duration
	var commitNanos int64
	if ts, err := gitnotes.CommitTimestampNanos(repoPath, sha); err == nil {
		commitNanos = ts
	}
	sessionNanos := db.SessionDurationNanos(repoID, commitNanos)

	w := os.Stdout
	renderStats(w, &note, meta, sessionNanos)
	return nil
}

type commitMeta_ map[string]string

func commitMeta(repoPath, sha string) (commitMeta_, error) {
	out, err := exec.Command("git", "-C", repoPath, "show", "-s",
		"--format=%H|%s|%ae|%ci", sha).Output()
	if err != nil {
		return nil, err
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "|", 4)
	m := commitMeta_{}
	keys := []string{"sha", "subject", "author", "date"}
	for i, k := range keys {
		if i < len(parts) {
			m[k] = parts[i]
		}
	}
	return m, nil
}

func readNote(repoPath, sha string) ([]byte, error) {
	return exec.Command("git", "-C", repoPath, "notes", "--ref="+gitnotes.NotesRef, "show", sha).Output()
}

func renderStats(w io.Writer, note *gitnotes.Note, meta commitMeta_, sessionNanos int64) {
	sha := meta["sha"]
	if len(sha) > 12 {
		sha = sha[:12]
	}
	subject := meta["subject"]
	author := meta["author"]
	dateStr := meta["date"]

	// Header
	ago := ""
	if t, err := time.Parse("2006-01-02 15:04:05 -0700", dateStr); err == nil {
		ago = fmt.Sprintf("  (%s)", humanDuration(time.Since(t)))
	}
	fmt.Fprintf(w, "%s %s  %q%s\n", bold("commit"), dim(sha), subject, dim(ago))
	if author != "" {
		fmt.Fprintf(w, "  %s\n", dim("author: "+author))
	}
	if note.Branch != "" {
		fmt.Fprintf(w, "  %s\n", dim("branch: "+note.Branch))
	}
	fmt.Fprintln(w)

	// Changes
	net := note.Totals.AILines + note.Totals.HumanLines - note.Totals.DeletedLines
	fmt.Fprintf(w, "%s\n", bold("Changes:"))
	fmt.Fprintf(w, "  %s  %s\n",
		green(fmt.Sprintf("+%-4d added", note.Totals.AILines+note.Totals.HumanLines)),
		dim(fmt.Sprintf("(AI %d · human %d)", note.Totals.AILines, note.Totals.HumanLines)))
	if note.Totals.DeletedLines > 0 {
		fmt.Fprintf(w, "  %s\n", red(fmt.Sprintf("-%-4d deleted", note.Totals.DeletedLines)))
	}
	fmt.Fprintf(w, "  ─────────\n")
	fmt.Fprintf(w, "  %s net\n", bold(fmt.Sprintf("%-5d", net)))
	fmt.Fprintln(w)

	// AI attribution
	if note.Totals.AILines > 0 {
		fmt.Fprintf(w, "%s\n", bold("AI attribution:"))
		for _, name := range []string{"claude", "cursor", "codex", "copilot", "gemini"} {
			t, ok := note.ByTool[name]
			if !ok || t.Lines == 0 {
				continue
			}
			modelStr := ""
			if t.Model != nil && *t.Model != "" {
				modelStr = "  " + dim(*t.Model)
			}
			tokStr := ""
			if t.Tokens != nil {
				tokStr = fmt.Sprintf("  %s", dim(fmt.Sprintf("in=%s out=%s cache=%s",
					formatK(t.Tokens.Input), formatK(t.Tokens.Output), formatK(t.Tokens.CacheRead))))
			}
			acceptStr := ""
			if t.SuggestedLines > 0 {
				acceptStr = "  " + dim(fmt.Sprintf("suggested=%d accepted=%d", t.SuggestedLines, t.AcceptedLines))
			}
			genType := toolGenType(note, name)
			if genType == "" {
				// Fall back to the per-tool heuristic for tools that don't
				// emit gen_type yet (e.g. legacy edits without per-line tags).
				genType = legacyToolGenType(name)
			}
			fmt.Fprintf(w, "  %-10s %4d lines  %-12s%s%s%s\n",
				name, t.Lines, dim(genType), modelStr, tokStr, acceptStr)
		}
		fmt.Fprintln(w)
	}

	// Generation type
	g := note.ByGenType
	if g.Chat+g.CLI+g.Completion > 0 {
		fmt.Fprintf(w, "%s\n", bold("Generation:"))
		if g.Chat > 0 {
			fmt.Fprintf(w, "  chat        %4d lines\n", g.Chat)
		}
		if g.CLI > 0 {
			fmt.Fprintf(w, "  cli         %4d lines\n", g.CLI)
		}
		if g.Completion > 0 {
			fmt.Fprintf(w, "  completion  %4d lines\n", g.Completion)
		}
		fmt.Fprintln(w)
	}

	// Files
	if len(note.Files) > 0 {
		fmt.Fprintf(w, "%s\n", bold("Files:"))
		for _, f := range note.Files {
			toolBreakdown := fileToolBreakdown(f)
			fmt.Fprintf(w, "  %-32s %s%s  %s\n",
				f.Path,
				green(fmt.Sprintf("+%-3d", f.Added)),
				red(fmt.Sprintf("-%-3d", f.Deleted)),
				dim(toolBreakdown))
		}
		fmt.Fprintln(w)
	}

	// Tokens total
	if note.Totals.Tokens != nil {
		t := note.Totals.Tokens
		fmt.Fprintf(w, "%s  in=%s  out=%s  cache_read=%s  cache_write=%s\n",
			bold("Tokens:"),
			formatK(t.Input), formatK(t.Output), formatK(t.CacheRead), formatK(t.CacheWrite))
	}

	// Session / coding time. Prefer the value baked into the note (captured
	// at attribution time); fall back to the live DB lookup for older notes.
	coding := note.CodingTimeNanos
	if coding == 0 {
		coding = sessionNanos
	}
	if coding > 0 {
		mins := coding / int64(time.Minute)
		fmt.Fprintf(w, "%s  ~%d min  (first edit → commit)\n", bold("Coding time:"), mins)
	}
}

// toolGenType returns the dominant generation type for a tool across the note,
// derived from per-line gen_type tags. Falls back to "" when the tool didn't
// produce any classified lines.
func toolGenType(note *gitnotes.Note, tool string) string {
	counts := map[string]int{}
	for _, f := range note.Files {
		for _, l := range f.Lines {
			if l.Tool != tool || l.GenType == nil {
				continue
			}
			counts[*l.GenType] += l.NumLines()
		}
	}
	var best string
	var bestN int
	for k, n := range counts {
		if n > bestN {
			best = k
			bestN = n
		}
	}
	return best
}

// legacyToolGenType is the pre-per-line-gen_type fallback used only when the
// note doesn't have any classified lines for the tool (i.e. older notes
// produced before the schema upgrade). Avoid in new code; per-line gen_type
// is authoritative.
func legacyToolGenType(tool string) string {
	switch tool {
	case "codex":
		return "cli"
	case "copilot":
		return "completion"
	default:
		return "chat"
	}
}

// fileToolBreakdown summarises which tools contributed to a file. Deletions
// (Tool == "") are ignored — they don't carry attribution.
func fileToolBreakdown(f gitnotes.FileEntry) string {
	counts := map[string]int{}
	for _, l := range f.Lines {
		if l.Type != "add" || l.Tool == "" {
			continue
		}
		counts[l.Tool] += l.NumLines()
	}
	var parts []string
	for _, name := range []string{"claude", "cursor", "codex", "copilot", "gemini", "human"} {
		if c := counts[name]; c > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", name, c))
		}
	}
	return strings.Join(parts, " · ")
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func formatK(n int64) string {
	if n == 0 {
		return "0"
	}
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}
