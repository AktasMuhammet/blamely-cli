package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/blamely/blamely/internal/gitnotes"
)

// RenderCommit prints a line-by-line attribution view for the given commit
// by reading its blamely git note.
func RenderCommit(sha string) error {
	// Use cwd as repo path.
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return fmt.Errorf("not a git repo: %w", err)
	}
	repo := strings.TrimSpace(string(out))

	body, err := readNote(repo, sha)
	if err != nil {
		return fmt.Errorf("no blamely note for %s: %w", sha, err)
	}
	var note gitnotes.Note
	if err := json.Unmarshal(body, &note); err != nil {
		return fmt.Errorf("parse note: %w", err)
	}
	meta, err := commitMeta(repo, sha)
	if err != nil {
		meta = commitMeta_{"sha": sha}
	}
	renderDashboard(os.Stdout, &note, meta, true)
	return nil
}

func RenderSince(since string) error {
	// Aggregated table across recent commits is deferred until basic flow works.
	return fmt.Errorf("report --since=%s: aggregated rollup not yet implemented", since)
}

// RenderCommitHTML reads the blamely note for sha, renders the self-contained
// HTML dashboard, writes it to outPath (a temp file when empty), optionally
// opens it in the default browser, and returns the file path it wrote.
func RenderCommitHTML(sha, outPath string, openFile bool) (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("not a git repo: %w", err)
	}
	repo := strings.TrimSpace(string(out))

	noteBytes, err := readNote(repo, sha)
	if err != nil {
		return "", fmt.Errorf("no blamely note for %s: run `blamely attribute %s %s` first", sha, repo, sha)
	}
	var note gitnotes.Note
	if err := json.Unmarshal(noteBytes, &note); err != nil {
		return "", fmt.Errorf("parse note: %w", err)
	}
	meta, err := commitMeta(repo, sha)
	if err != nil {
		meta = commitMeta_{"sha": sha}
	}

	html, err := RenderHTML(&note, meta)
	if err != nil {
		return "", err
	}

	if outPath == "" {
		short := note.Commit
		if len(short) > 12 {
			short = short[:12]
		}
		if short == "" {
			short = "report"
		}
		outPath = filepath.Join(os.TempDir(), "blamely-report-"+short+".html")
	}
	if err := os.WriteFile(outPath, []byte(html), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", outPath, err)
	}
	if openFile {
		_ = openInBrowser(outPath) // best-effort: the path is printed regardless
	}
	return outPath, nil
}

// openInBrowser launches the OS default handler for path. Best-effort: it
// returns the launcher's start error but never blocks (the report path is
// printed either way).
func openInBrowser(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	return cmd.Start()
}

// printNote renders the full per-commit attribution view: the same premium
// header + AI/Human bar + per-tool/generation breakdown that `blamely
// attribute` prints (via RenderBar, so the visual language stays consistent
// across commands), followed by report's distinguishing detail — coding time,
// the model rollup, the conversation, and a clean per-file, per-range
// line-level attribution listing.
func printNote(n *gitnotes.Note) {
	w := os.Stdout
	RenderBar(w, n, 40)
	fmt.Fprintln(w)

	if n.CodingTimeNanos > 0 {
		inlineMeta(w, "Coding", dim(formatDuration(time.Duration(n.CodingTimeNanos))+"   first edit "+glyphArr+" commit"))
	}
	if len(n.Totals.Models) > 0 {
		inlineMeta(w, "Models", formatModels(n.Totals.Models))
	}

	if len(n.Files) == 0 {
		fmt.Fprintln(w)
		versionLine(w, noteVersion(n))
		return
	}
	sectionHead(w, "Files")
	for _, f := range n.Files {
		fmt.Fprintln(w)
		printFileHeader(w, f)
		for _, l := range f.Lines {
			loc := fmt.Sprintf("L%d", l.Start)
			if l.End > l.Start {
				loc = fmt.Sprintf("L%d-%d", l.Start, l.End)
			}
			fmt.Fprintf(w, "%s  %s  %s  %s  %s\n", gutter,
				dim(glyphBar),
				dim(fmt.Sprintf("%-10s", loc)),
				dim(fmt.Sprintf("%-6s", l.Type)),
				formatAttribution(l))
		}
	}
	fmt.Fprintln(w)
	versionLine(w, noteVersion(n))
	fmt.Fprintln(w)
}

// printFileHeader renders one file's status line:
//
//	app.js  [ADDED]                              +122  -0
//	server.go  [RENAMED] (from old_server.go)     +18  -4
func printFileHeader(w io.Writer, f gitnotes.FileEntry) {
	name := bold(f.Path)
	if f.Type != "" {
		name += "  " + dim("["+f.Type+"]")
	}
	if f.RenamedFrom != "" {
		name += "  " + dim("(from "+f.RenamedFrom+")")
	}
	if f.CopiedFrom != "" {
		name += "  " + dim("(copied from "+f.CopiedFrom+")")
	}
	fmt.Fprintf(w, "%s%s  %s %s\n", gutter, name, green(fmt.Sprintf("+%-4d", f.Added)), red(fmt.Sprintf("-%-4d", f.Deleted)))
}

// formatAttribution renders one range's authorship as a single colored token:
// "human" in blue for human-typed/pasted lines, or "<tool> · <model> ·
// toolLabel maps an internal tool id to its user-facing label. Only copypaste
// is special-cased — the stored id "copypaste" reads as "Copy&Paste" in every
// report surface; AI tool ids render as-is. Centralised so the label is
// identical across the terminal, dashboard, and HTML renderers.
func toolLabel(tool string) string {
	if tool == "copypaste" {
		return "Copy&Paste"
	}
	return tool
}

// <gen_type>" in green for AI-attributed lines (model/gen_type dimmed, omitted
// when unknown). Deletions (no AuthorType) render as a dim "—".
func formatAttribution(l gitnotes.RangeEntry) string {
	switch {
	case l.Tool == "copypaste":
		// copypaste is human-authored (author_type Human) — render in the human
		// colour with a copy-paste note, not as a green AI tool.
		return blue("human") + dim(" · copy-paste")
	case l.Tool != "":
		s := green(toolLabel(l.Tool))
		var extra []string
		if l.Model != nil && *l.Model != "" {
			extra = append(extra, *l.Model)
		}
		if l.GenType != nil && *l.GenType != "" {
			extra = append(extra, *l.GenType)
		}
		if len(extra) > 0 {
			s += dim(" · " + strings.Join(extra, " · "))
		}
		return s
	case l.AuthorType != "":
		return blue("human")
	default:
		return dim("—")
	}
}

// formatModels renders the commit's per-model line-count rollup, busiest
// model first: "claude-opus-4-7 (320 lines)  ·  gpt-5.5 (48 lines)".
func formatModels(models map[string]int) string {
	type entry struct {
		name  string
		count int
	}
	entries := make([]entry, 0, len(models))
	for name, count := range models {
		entries = append(entries, entry{name, count})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].count != entries[j].count {
			return entries[i].count > entries[j].count
		}
		return entries[i].name < entries[j].name
	})
	parts := make([]string, len(entries))
	for i, e := range entries {
		parts[i] = e.name + "  " + dim(fmt.Sprintf("(%d lines)", e.count))
	}
	return strings.Join(parts, "  ·  ")
}

// formatDuration renders a duration as "Xh Ym" / "Ym Ss" / "Ss" depending on
// magnitude. Used for the coding-time summary in printNote.
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		mins := int(d.Minutes())
		secs := int(d.Seconds()) - mins*60
		if secs == 0 {
			return fmt.Sprintf("%dm", mins)
		}
		return fmt.Sprintf("%dm %ds", mins, secs)
	}
	hours := int(d.Hours())
	mins := int(d.Minutes()) - hours*60
	if mins == 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dh %dm", hours, mins)
}
