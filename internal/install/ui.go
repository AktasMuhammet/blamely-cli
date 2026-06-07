package install

import (
	"fmt"
	"os"
)

// ANSI styling for the `blamely install` log. Mirrors the NO_COLOR convention
// used by internal/report/bar.go (https://no-color.org): color is on by
// default and disabled when NO_COLOR is set, so piped/CI output stays plain.
const (
	uiReset = "\x1b[0m"
	uiBold  = "\x1b[1m"
	uiDim   = "\x1b[2m"
	uiGreen = "\x1b[32m"
	uiRed   = "\x1b[31m"
)

func uiColor() bool {
	return os.Getenv("NO_COLOR") == ""
}

// section prints a group heading (e.g. "Hooks", "Editors", "System") that
// visually separates the install log into the scannable groups a user actually
// cares about, instead of one long undifferentiated stream of checkmarks.
func section(title string) {
	fmt.Println()
	if uiColor() {
		fmt.Printf("%s%s%s\n", uiBold, title, uiReset)
	} else {
		fmt.Println(title)
	}
}

// ok/info/fail render one aligned row under the current section: a coloured
// status glyph, a left-aligned label, and a dimmed detail string. Using the
// same three states everywhere (done / skipped-or-already-present / failed)
// keeps the whole log visually consistent regardless of which subsystem wrote
// the line.
func ok(label, detail string)   { uiRow(uiGreen, "✓", label, detail) }
func info(label, detail string) { uiRow(uiDim, "•", label, detail) }
func fail(label, detail string) { uiRow(uiRed, "✗", label, detail) }

func uiRow(color, glyph, label, detail string) {
	mark := glyph
	if uiColor() {
		mark = color + glyph + uiReset
	}
	if detail == "" {
		fmt.Printf("  %s %s\n", mark, label)
		return
	}
	d := detail
	if uiColor() {
		d = uiDim + detail + uiReset
	}
	fmt.Printf("  %s %-24s %s\n", mark, label, d)
}
