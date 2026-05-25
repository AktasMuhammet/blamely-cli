package report

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/blamely/blamely/internal/gitnotes"
)

const (
	ansiReset = "\x1b[0m"
	ansiGreen = "\x1b[32m"
	ansiBlue  = "\x1b[34m"
	ansiDim   = "\x1b[2m"
	ansiBold  = "\x1b[1m"
)

// RenderBar writes a commit attribution summary to w. Layout (color mode):
//
//	blamely  ·  abc123def456  ·  main  ·  "commit message"
//
//	AI 72%  [████████████████████████████░░░░░░░░░░░░]  Human 28%
//	  claude 60 (claude-opus-4-7) — 1200 in / 500 out tok
//
//	Generation:
//	  chat         72  ████████████████████  72.7%
//	  cli           0  ░░░░░░░░░░░░░░░░░░░░
//	  completion   27  ████████░░░░░░░░░░░░  27.3%
//	  human         0  ░░░░░░░░░░░░░░░░░░░░
//
// Color is on by default; disable with NO_COLOR=1. Width defaults to 40.
func RenderBar(w io.Writer, note *gitnotes.Note, width int) {
	if width <= 0 {
		width = 40
	}
	ai := note.Totals.AILines
	hu := note.Totals.HumanLines
	del := note.Totals.DeletedLines
	total := ai + hu
	color := colorEnabled()

	// Commit header — rendered when there's a SHA so the bar is self-identifying.
	if note.Commit != "" {
		sha := note.Commit
		if len(sha) > 12 {
			sha = sha[:12]
		}
		parts := []string{sha}
		if note.Branch != "" {
			parts = append(parts, note.Branch)
		}
		if note.Message != "" {
			subject := strings.SplitN(note.Message, "\n", 2)[0]
			if len(subject) > 52 {
				subject = subject[:49] + "..."
			}
			parts = append(parts, "\""+subject+"\"")
		}
		header := "blamely  ·  " + strings.Join(parts, "  ·  ")
		if color {
			fmt.Fprintf(w, "\n%s%s%s\n\n", ansiBold, header, ansiReset)
		} else {
			fmt.Fprintf(w, "\n%s\n\n", header)
		}
	}

	if total == 0 {
		if del == 0 {
			// Empty commit: render an all-dim track so the column stays consistent.
			label := "AI vs Human: (no changes)"
			var bar string
			if color {
				bar = ansiDim + strings.Repeat("░", width) + ansiReset
				label = ansiDim + label + ansiReset
			} else {
				bar = strings.Repeat("-", width)
			}
			fmt.Fprintf(w, "%s  [%s]\n", label, bar)
			return
		}
		// Deletion-only commit. Deletions are 100% human by convention.
		humanBar := strings.Repeat("-", width)
		humanLabel := fmt.Sprintf("Human 100%% (%d deleted)", del)
		if color {
			humanBar = ansiBlue + strings.Repeat("░", width) + ansiReset
			humanLabel = ansiBlue + ansiBold + humanLabel + ansiReset
		}
		fmt.Fprintf(w, "%s  [%s]\n", humanLabel, humanBar)
		return
	}

	// Main AI/Human stacked bar.
	aiCells := (ai*width + total/2) / total
	if aiCells > width {
		aiCells = width
	}
	huCells := width - aiCells

	var aiPart, huPart string
	if color {
		aiPart = ansiGreen + strings.Repeat("█", aiCells) + ansiReset
		huPart = ansiBlue + strings.Repeat("░", huCells) + ansiReset
	} else {
		aiPart = strings.Repeat("#", aiCells)
		huPart = strings.Repeat("-", huCells)
	}

	aiPct := float64(ai) * 100 / float64(total)
	huPct := float64(hu) * 100 / float64(total)

	aiLabel := fmt.Sprintf("AI %.0f%%", aiPct)
	huLabel := fmt.Sprintf("Human %.0f%%", huPct)
	if color {
		aiLabel = ansiGreen + ansiBold + aiLabel + ansiReset
		huLabel = ansiBlue + ansiBold + huLabel + ansiReset
	}

	fmt.Fprintf(w, "%s  [%s%s]  %s\n", aiLabel, aiPart, huPart, huLabel)

	// Deletions side note — not attributed per-line, treated as human.
	if del > 0 {
		delLabel := fmt.Sprintf("Deleted: %d lines (treated as 100%% human)", del)
		if color {
			delLabel = ansiDim + delLabel + ansiReset
		}
		fmt.Fprintln(w, "  "+delLabel)
	}

	// Per-tool breakdown when at least one AI tool has non-zero lines.
	var aiToolLines []string
	for _, name := range []string{"claude", "cursor", "codex", "copilot", "gemini"} {
		t, ok := note.ByTool[name]
		if !ok || t.Lines == 0 {
			continue
		}
		line := fmt.Sprintf("%s %d", name, t.Lines)
		if t.Model != nil && *t.Model != "" {
			line += " (" + *t.Model + ")"
		}
		if t.Tokens != nil {
			line += fmt.Sprintf(" — %d in / %d out tok", t.Tokens.Input, t.Tokens.Output)
		}
		aiToolLines = append(aiToolLines, line)
	}
	if len(aiToolLines) > 0 {
		prefix := "  "
		if color {
			prefix = ansiDim + "  "
		}
		suffix := ""
		if color {
			suffix = ansiReset
		}
		for _, l := range aiToolLines {
			fmt.Fprintf(w, "%s%s%s\n", prefix, l, suffix)
		}
	}

	// Generation-type breakdown. Always rendered when there are added lines so
	// the breakdown shows the full picture including modes with zero contribution.
	// Uses █/░ block chars (not # or -) so the main-bar char counts stay clean.
	g := note.ByGenType
	genSum := g.Chat + g.CLI + g.Completion + g.Human
	if genSum == 0 {
		genSum = 1 // avoid division by zero; all bars render as empty
	}

	fmt.Fprintln(w)
	genLabel := "Generation:"
	if color {
		genLabel = ansiBold + genLabel + ansiReset
	}
	fmt.Fprintln(w, genLabel)

	const miniBarW = 20
	type genRow struct {
		name  string
		lines int
		clr   string
	}
	genRows := []genRow{
		{"chat      ", g.Chat, ansiGreen},
		{"cli       ", g.CLI, "\x1b[36m"},       // cyan
		{"completion", g.Completion, "\x1b[35m"}, // magenta
		{"human     ", g.Human, ansiBlue},
	}
	for _, r := range genRows {
		filled := int(float64(miniBarW)*float64(r.lines)/float64(genSum) + 0.5)
		if filled > miniBarW {
			filled = miniBarW
		}
		pctStr := ""
		if r.lines > 0 {
			pct := float64(r.lines) * 100 / float64(genSum)
			pctStr = fmt.Sprintf("  %5.1f%%", pct)
		}
		var bar string
		if color {
			if r.lines > 0 {
				bar = r.clr + strings.Repeat("█", filled) +
					ansiDim + strings.Repeat("░", miniBarW-filled) + ansiReset
			} else {
				bar = ansiDim + strings.Repeat("░", miniBarW) + ansiReset
			}
		} else {
			bar = strings.Repeat("█", filled) + strings.Repeat("░", miniBarW-filled)
		}
		fmt.Fprintf(w, "  %-12s %4d  %s%s\n", r.name, r.lines, bar, pctStr)
	}
}

// colorEnabled reports whether ANSI color codes should be emitted.
// On by default, off when NO_COLOR is set (https://no-color.org).
func colorEnabled() bool {
	return os.Getenv("NO_COLOR") == ""
}
