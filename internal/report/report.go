package report

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

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
	for _, name := range []string{"claude", "cursor", "codex", "copilot", "gemini", "human"} {
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
