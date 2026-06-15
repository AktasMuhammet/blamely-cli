package report

import (
	"fmt"
	"io"
	"strings"
)

// Premium shared rendering primitives. Every detail view (stats, blame, commit
// report) renders through these so the look stays consistent: a brand title bar
// with a hairline rule, light section headers, a steady two-space gutter, and
// one restrained accent color. Keeping them here means a tweak to the visual
// language lands everywhere at once.

const (
	gutter   = "  "
	glyphDot = "·"
	glyphBar = "│"
	glyphArr = "→"
	ruleW    = 58
)

// Version is the running blamely version, shown in report footers so the output
// is self-identifying. Defaults to "dev"; cmd/blamely overwrites it at startup
// with the real resolved version.
var Version = "dev"

// versionLine renders the dim footer that stamps the report with the blamely
// version that GENERATED the note's attribution (pass noteVersion(note)), not the
// version rendering the report — so a footer on an old commit credits the version
// that actually produced it.
func versionLine(w io.Writer, version string) {
	fmt.Fprintf(w, "%s%s\n", gutter, dim("blamely "+version))
}

// accent is the single brand color used for titles and the active value in a
// header — cyan, bold. Everything else stays default-weight or dimmed so the
// accent actually reads as emphasis.
func accent(s string) string { return style(ansiCyan+ansiBold, s) }

// hairline returns a dim horizontal rule of n cells.
func hairline(n int) string {
	if n <= 0 {
		n = ruleW
	}
	return dim(strings.Repeat("─", n))
}

// titleBar prints the identity line — "blamely <verb> · <subject>" in the brand
// accent — under a leading blank line, followed by a hairline rule.
func titleBar(w io.Writer, verb, subject string) {
	title := "blamely " + verb
	if subject != "" {
		title += "  " + glyphDot + "  " + subject
	}
	fmt.Fprintf(w, "\n%s%s\n", gutter, accent(title))
	fmt.Fprintf(w, "%s%s\n", gutter, hairline(ruleW))
}

// sectionHead prints a blank line then a section header (bold), the consistent
// divider between the blocks of a detail view.
func sectionHead(w io.Writer, label string) {
	fmt.Fprintf(w, "\n%s%s\n", gutter, bold(label))
}

// metaRow prints an aligned "key   value" line under the title block, e.g.
// "author   Jane Dev". The key is dimmed and padded to keyW.
func metaRow(w io.Writer, key, val string) {
	if val == "" {
		return
	}
	fmt.Fprintf(w, "%s%s  %s\n", gutter, dim(fmt.Sprintf("%-7s", key)), val)
}

// inlineMeta prints a single-line "Label   value" summary row (used for the
// one-liners like Tokens / Coding), label dimmed-bold and left-padded to align
// with section bodies.
func inlineMeta(w io.Writer, label, val string) {
	fmt.Fprintf(w, "%s%s  %s\n", gutter, bold(fmt.Sprintf("%-7s", label)), val)
}

// sep joins parts with a dim middot, the standard inline separator.
func sep(parts ...string) string {
	kept := parts[:0]
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, dim("  "+glyphDot+"  "))
}
