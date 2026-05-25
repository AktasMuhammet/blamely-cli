package report

import (
	"encoding/json"
	"fmt"
	"os/exec"
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

func printNote(n *gitnotes.Note) {
	fmt.Printf("commit %s\n", n.Commit)
	if n.Branch != "" {
		fmt.Printf("  branch:        %s\n", n.Branch)
	}
	if n.Message != "" {
		// Show the subject line only (first line) plus a hint that the body
		// is in the note. Avoids printing multi-line messages in the header
		// summary.
		subject := strings.SplitN(n.Message, "\n", 2)[0]
		fmt.Printf("  message:       %s\n", subject)
	}
	if n.CodingTimeNanos > 0 {
		fmt.Printf("  coding time:   %s\n", formatDuration(time.Duration(n.CodingTimeNanos)))
	}
	fmt.Printf("  AI lines:      %d\n", n.Totals.AILines)
	fmt.Printf("  human lines:   %d\n", n.Totals.HumanLines)
	if n.Totals.DeletedLines > 0 {
		// Deletions are line-level but not tool-attributed today; they count
		// as human actions in the bar/totals.
		fmt.Printf("  deleted lines: %d  (treated as human)\n", n.Totals.DeletedLines)
	}
	fmt.Printf("  files:         %d\n", n.Totals.Files)
	if n.Totals.Tokens != nil {
		t := n.Totals.Tokens
		fmt.Printf("  tokens:        in=%d out=%d cache_read=%d cache_write=%d\n",
			t.Input, t.Output, t.CacheRead, t.CacheWrite)
	}
	fmt.Println()
	if len(n.Totals.Models) > 0 {
		fmt.Println("  models:")
		for model, count := range n.Totals.Models {
			fmt.Printf("    %-24s %4d lines\n", model, count)
		}
	}
	fmt.Println()

	fmt.Println("by tool:")
	for _, name := range []string{"claude", "cursor", "codex", "copilot", "gemini", "human", "copypaste"} {
		t, ok := n.ByTool[name]
		if !ok {
			continue
		}
		modelStr := "-"
		if t.Model != nil {
			modelStr = *t.Model
		}
		fmt.Printf("  %-8s %4d lines  model=%s", name, t.Lines, modelStr)
		if t.SuggestedLines > 0 {
			fmt.Printf("  suggested=%d accepted=%d", t.SuggestedLines, t.AcceptedLines)
		}
		if t.Tokens != nil {
			fmt.Printf("  tokens(in=%d out=%d)", t.Tokens.Input, t.Tokens.Output)
		}
		fmt.Println()
	}
	fmt.Println()

	if len(n.Conversation) > 0 {
		printConversation(n.Conversation)
	}

	for _, f := range n.Files {
		header := f.Path
		if f.Type != "" {
			header = fmt.Sprintf("%s  [%s]", f.Path, f.Type)
		}
		if f.RenamedFrom != "" {
			header += fmt.Sprintf("  (from %s)", f.RenamedFrom)
		}
		if f.CopiedFrom != "" {
			header += fmt.Sprintf("  (copied from %s)", f.CopiedFrom)
		}
		fmt.Printf("%s  +%d -%d\n", header, f.Added, f.Deleted)
		for _, l := range f.Lines {
			modelStr := ""
			if l.Model != nil {
				modelStr = "  " + *l.Model
			}
			genTypeStr := ""
			if l.GenType != nil {
				genTypeStr = "  " + *l.GenType
			}
			toolStr := l.Tool
			if toolStr == "" {
				toolStr = "-"
			}
			author := l.AuthorType
			if author == "" {
				author = "-"
			}
			fmt.Printf("  L%-6d %-7s %-6s %-8s%s%s\n",
				l.Line, l.Type, author, toolStr, modelStr, genTypeStr)
		}
		fmt.Println()
	}
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
