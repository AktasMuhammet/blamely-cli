package report

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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

	cmd := exec.Command("git", "-C", repo, "notes", "--ref="+gitnotes.NotesRef, "show", sha)
	body, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("no blamely note for %s: %w", sha, err)
	}
	var note gitnotes.Note
	if err := json.Unmarshal(body, &note); err != nil {
		return fmt.Errorf("parse note: %w", err)
	}
	printNote(&note)
	return nil
}

func RenderSince(since string) error {
	// Aggregated table across recent commits is deferred until basic flow works.
	return fmt.Errorf("report --since=%s: aggregated rollup not yet implemented", since)
}

// printNote renders the full per-commit attribution view: the same premium
// header + AI/Human bar + per-tool/generation breakdown that `blamely
// attribute` prints (via RenderBar, so the visual language stays consistent
// across commands), followed by report's distinguishing detail — coding time,
// the model rollup, the conversation, and a clean per-file, per-range
// line-level attribution listing.
func printNote(n *gitnotes.Note) {
	RenderBar(os.Stdout, n, 40)
	fmt.Println()

	if n.CodingTimeNanos > 0 {
		fmt.Printf("  %s  %s\n", bold("Coding time:"), formatDuration(time.Duration(n.CodingTimeNanos)))
	}
	if len(n.Totals.Models) > 0 {
		fmt.Printf("  %s  %s\n", bold("Models:"), formatModels(n.Totals.Models))
	}
	fmt.Println()

	if len(n.Conversation) > 0 {
		printConversation(n.Conversation)
	}

	if len(n.Files) == 0 {
		return
	}
	fmt.Println(bold("Files:"))
	for _, f := range n.Files {
		fmt.Println()
		printFileHeader(f)
		for _, l := range f.Lines {
			loc := fmt.Sprintf("L%d", l.Start)
			if l.End > l.Start {
				loc = fmt.Sprintf("L%d-%d", l.Start, l.End)
			}
			fmt.Printf("    %s  %s  %s\n", dim(fmt.Sprintf("%-9s", loc)), dim(fmt.Sprintf("%-6s", l.Type)), formatAttribution(l))
		}
	}
	fmt.Println()
}

// printFileHeader renders one file's status line:
//
//	app.js  [ADDED]                              +122  -0
//	server.go  [RENAMED] (from old_server.go)     +18  -4
func printFileHeader(f gitnotes.FileEntry) {
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
	fmt.Printf("%s  %s %s\n", name, green(fmt.Sprintf("+%-4d", f.Added)), red(fmt.Sprintf("-%-4d", f.Deleted)))
}

// formatAttribution renders one range's authorship as a single colored token:
// "human" in blue for human-typed/pasted lines, or "<tool> · <model> ·
// <gen_type>" in green for AI-attributed lines (model/gen_type dimmed, omitted
// when unknown). Deletions (no AuthorType) render as a dim "—".
func formatAttribution(l gitnotes.RangeEntry) string {
	switch {
	case l.Tool != "":
		s := green(l.Tool)
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

// printConversation renders the user/assistant conversation in a clean,
// premium terminal format. User prompts are shown in bold; assistant replies
// are dimmed so the user's intent reads first, then the AI response.
//
// Layout (color on):
//
//	 Conversation ────────────────────────────────
//
//	  ╷ User
//	  │ How should I structure the auth middleware?
//
//	  ╷ claude-opus-4-7
//	  │ I'll implement a JWT middleware that validates...
//
func printConversation(turns []gitnotes.ConvTurn) {
	color := colorEnabled()
	sep := strings.Repeat("─", 44)
	label := "Conversation"
	if color {
		label = ansiBold + label + ansiReset
	}
	fmt.Printf("%s %s\n\n", label, sep)

	for _, t := range turns {
		roleLabel := t.Role
		var roleColor, textColor string
		if color {
			if t.Role == "user" {
				roleColor = ansiBold
			} else {
				roleColor = "\x1b[36m" + ansiBold // cyan bold for assistant
			}
			textColor = ansiReset
		}

		if color {
			fmt.Printf("  %s╷%s %s%s%s\n", ansiDim, ansiReset, roleColor, roleLabel, ansiReset)
		} else {
			fmt.Printf("  ╷ %s\n", roleLabel)
		}

		// Wrap the text at 68 chars so it fits in a standard 80-col terminal.
		words := strings.Fields(t.Text)
		line := ""
		for _, w := range words {
			if len(line)+1+len(w) > 68 && line != "" {
				if color {
					fmt.Printf("  %s│%s %s%s%s\n", ansiDim, ansiReset, textColor, line, ansiReset)
				} else {
					fmt.Printf("  │ %s\n", line)
				}
				line = w
			} else {
				if line == "" {
					line = w
				} else {
					line += " " + w
				}
			}
		}
		if line != "" {
			if color {
				fmt.Printf("  %s│%s %s%s%s\n", ansiDim, ansiReset, textColor, line, ansiReset)
			} else {
				fmt.Printf("  │ %s\n", line)
			}
		}
		fmt.Println()
	}
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
