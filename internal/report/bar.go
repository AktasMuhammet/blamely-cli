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

// RenderBar writes a stacked horizontal bar comparing AI vs Human lines in
// the commit covered by `note`. Layout:
//
//	AI 59% (20)  [████████████████████░░░░░░░░░░░░░░░░░░░░]  Human 41% (14)
//
// The AI label is on the LEFT of the bar (green) and the Human label is on
// the RIGHT (blue). Color is on by default; disable it with NO_COLOR=1.
// Width defaults to 40 cells.
func RenderBar(w io.Writer, note *gitnotes.Note, width int) {
	if width <= 0 {
		width = 40
	}
	ai := note.Totals.AILines
	hu := note.Totals.HumanLines
	del := note.Totals.DeletedLines
	total := ai + hu
	color := colorEnabled()

	if total == 0 {
		if del == 0 {
			fmt.Fprintln(w, "AI vs Human: (no changes)")
			return
		}
		// Deletion-only commit. We don't currently attribute deletions per-line,
		// so by convention they count as a 100% human action (deleting code is
		// always a deliberate user act, even when the line being removed came
		// from an AI originally).
		humanCells := strings.Repeat("-", width)
		humanBar := humanCells
		humanLabel := fmt.Sprintf("Human 100%% (%d deleted)", del)
		if color {
			humanBar = ansiBlue + strings.Repeat("░", width) + ansiReset
			humanLabel = ansiBlue + ansiBold + humanLabel + ansiReset
		}
		fmt.Fprintf(w, "%s  [%s]\n", humanLabel, humanBar)
		return
	}

	// Round-half-up split so AI and Human cells sum to width exactly.
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

	aiLabel := fmt.Sprintf("AI %.0f%% (%d)", aiPct, ai)
	huLabel := fmt.Sprintf("Human %.0f%% (%d)", huPct, hu)
	if color {
		aiLabel = ansiGreen + ansiBold + aiLabel + ansiReset
		huLabel = ansiBlue + ansiBold + huLabel + ansiReset
	}

	// AI label on the LEFT, Human label on the RIGHT.
	fmt.Fprintf(w, "%s  [%s%s]  %s\n", aiLabel, aiPart, huPart, huLabel)

	// Deletions side note. We don't currently attribute deletions per-line, so
	// they're surfaced as a separate count rather than rolled into the bar.
	if del > 0 {
		delLabel := fmt.Sprintf("Deleted: %d lines (treated as 100%% human)", del)
		if color {
			delLabel = ansiDim + delLabel + ansiReset
		}
		fmt.Fprintln(w, "  "+delLabel)
	}

	// Per-tool breakdown when there's more than one AI tool with non-zero lines.
	var aiToolLines []string
	for _, name := range []string{"claude", "cursor", "codex", "copilot"} {
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

	// Generation-type breakdown — only printed when there are AI lines.
	if total == 0 || (note.ByGenType.Chat == 0 && note.ByGenType.CLI == 0 && note.ByGenType.Completion == 0) {
		return
	}
	label := "Generation:"
	if color {
		label = ansiBold + label + ansiReset
	}
	fmt.Fprintln(w, label)

	type genRow struct {
		name  string
		lines int
		color string
	}
	rows := []genRow{
		{"chat       ", note.ByGenType.Chat, ansiGreen},
		{"cli        ", note.ByGenType.CLI, "\x1b[36m"}, // cyan
		{"completion ", note.ByGenType.Completion, "\x1b[35m"}, // magenta
	}
	for _, r := range rows {
		if r.lines == 0 {
			continue
		}
		if color {
			fmt.Fprintf(w, "  %s%s%s%d lines\n", r.color, r.name, ansiReset, r.lines)
		} else {
			fmt.Fprintf(w, "  %s%d lines\n", r.name, r.lines)
		}
	}
}

// colorEnabled reports whether ANSI color codes should be emitted.
// On by default, off when NO_COLOR is set (https://no-color.org).
// We deliberately don't gate on isatty(stdout) because the bar is meant to
// be read directly even when captured by `git commit | tee` or similar; the
// few extra bytes are harmless and most terminals strip ANSI when redirected
// to a file via the user's own pipeline.
func colorEnabled() bool {
	return os.Getenv("NO_COLOR") == ""
}
