package install

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// asciiFold maps the non-ASCII glyphs/punctuation the install log uses to ASCII.
// The log is captured to ~/.blamely/last-install.log and shown by the native
// installers, which decode it through a legacy code page — Inno's finished-page
// memo reads the BOM-less file as ANSI, NSIS's nsExec pipes it, and a cp850/cp857
// console mis-decodes it — turning "✓" into mojibake like "Γ£ô". Folding to ASCII
// for those captured/piped destinations keeps the log readable everywhere.
var asciiFold = strings.NewReplacer(
	"✓", "+", "✗", "x", "•", "-", "⚠", "!",
	"→", "->", "│", "|", "—", "--", "–", "-", "·", "-",
	"’", "'", "‘", "'", "“", "\"", "”", "\"", "…", "...",
)

// asciiTranslit wraps a writer, folding those glyphs to ASCII on the way out. It
// reports the ORIGINAL byte count so io.MultiWriter (which requires n == len(p))
// stays happy even though the folded output may be a different length.
type asciiTranslit struct{ w io.Writer }

func (a asciiTranslit) Write(p []byte) (int, error) {
	if _, err := io.WriteString(a.w, asciiFold.Replace(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}

// isTerminal reports whether f is an interactive terminal (a character device)
// rather than a pipe or file. A real TTY renders UTF-8 fine, so it keeps the
// glyphs; a pipe/file (installer capture, `> log`, nsExec) gets ASCII-folded.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// baseOut is stdout, ASCII-folded when stdout is not an interactive terminal
// (e.g. launched hidden by the installer, or redirected).
func baseOut() io.Writer {
	if isTerminal(os.Stdout) {
		return os.Stdout
	}
	return asciiTranslit{os.Stdout}
}

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

// uiOut is where the install report is written (stdout by default). uiTeeToFile
// swaps in a MultiWriter so the same report is also captured to a file — the
// native installers read that file to show exactly what happened, WITHOUT any
// shell redirection (`… > log`), which is the pattern EDR/SmartScreen flag.
var (
	uiOut        io.Writer = baseOut()
	uiForcePlain bool
)

// uiTeeToFile also writes every subsequent section/ok/info/fail line to path,
// forcing plain (no-ANSI) output so the captured file is clean text an installer
// can display verbatim. Returns a stop func that restores stdout-only output and
// closes the file. Best-effort: on error, output is left unchanged.
func uiTeeToFile(path string) func() {
	f, err := os.Create(path)
	if err != nil {
		return func() {}
	}
	prevOut, prevPlain := uiOut, uiForcePlain
	// The captured file is ALWAYS ASCII-folded — installers read it through a
	// legacy code page, so UTF-8 glyphs would show as mojibake.
	uiOut = io.MultiWriter(prevOut, asciiTranslit{f})
	uiForcePlain = true
	return func() {
		uiOut, uiForcePlain = prevOut, prevPlain
		_ = f.Close()
	}
}

func uiColor() bool {
	if uiForcePlain {
		return false
	}
	return os.Getenv("NO_COLOR") == ""
}

// section prints a group heading (e.g. "Hooks", "Editors", "System") that
// visually separates the install log into the scannable groups a user actually
// cares about, instead of one long undifferentiated stream of checkmarks.
func section(title string) {
	fmt.Fprintln(uiOut)
	if uiColor() {
		fmt.Fprintf(uiOut, "%s%s%s\n", uiBold, title, uiReset)
	} else {
		fmt.Fprintln(uiOut, title)
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
		fmt.Fprintf(uiOut, "  %s %s\n", mark, label)
		return
	}
	d := detail
	if uiColor() {
		d = uiDim + detail + uiReset
	}
	fmt.Fprintf(uiOut, "  %s %-24s %s\n", mark, label, d)
}
