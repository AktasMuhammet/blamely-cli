package install

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/blamely/blamely/internal/config"
	"github.com/blamely/blamely/internal/daemon"
)

// Doctor runs a full self-check of the blamely installation: daemon health,
// per-tool hook files, the global git post-commit hook, the binary at the
// stable path, the PATH entry, and the SQLite DB.
//
// Output mirrors what people expect from `brew doctor` / `flutter doctor`: a
// flat list of green ticks and red crosses, followed by a recommendation
// block when anything is wrong.
//
// Doctor is intentionally read-only — it never reinstalls or restarts. The
// goal is to tell the user *what's broken* so they can decide what to do
// (often: re-run `blamely install`).
func Doctor(w io.Writer) error {
	d := &doctor{w: w}
	d.daemon()
	d.binary()
	d.gitHook()
	d.path()
	d.db()
	d.hooks()
	d.summary()
	return nil
}

type doctor struct {
	w        io.Writer
	problems []string // human-readable list of failures, used in the summary
}

func (d *doctor) ok(label, detail string) {
	if detail == "" {
		fmt.Fprintf(d.w, "  ✓ %s\n", label)
	} else {
		fmt.Fprintf(d.w, "  ✓ %-32s %s\n", label, detail)
	}
}

func (d *doctor) warn(label, detail, fix string) {
	fmt.Fprintf(d.w, "  ! %-32s %s\n", label, detail)
	if fix != "" {
		d.problems = append(d.problems, "  • "+label+": "+fix)
	}
}

func (d *doctor) bad(label, detail, fix string) {
	fmt.Fprintf(d.w, "  ✗ %-32s %s\n", label, detail)
	if fix != "" {
		d.problems = append(d.problems, "  • "+label+": "+fix)
	}
}

func (d *doctor) section(name string) {
	fmt.Fprintf(d.w, "\n%s:\n", name)
}

func (d *doctor) daemon() {
	d.section("Daemon")
	sock, err := daemon.WaitForReady(1500 * time.Millisecond)
	if err != nil {
		d.bad("blamely daemon", fmt.Sprintf("not responding (%v)", err),
			"`blamely install` to re-register, or check ~/.blamely/daemon.log")
		return
	}
	d.ok("blamely daemon", fmt.Sprintf("listening on %s", sock))
}

func (d *doctor) binary() {
	d.section("Binary")
	p, err := InstalledBinaryPath()
	if err != nil {
		d.bad("stable binary path", err.Error(), "")
		return
	}
	st, err := os.Stat(p)
	if err != nil {
		d.bad("stable binary path", fmt.Sprintf("missing (%s)", p),
			"run `blamely install` again to re-copy the binary")
		return
	}
	if st.Mode()&0o111 == 0 {
		d.warn("stable binary path", fmt.Sprintf("%s (not executable)", p),
			"chmod +x "+p)
		return
	}
	d.ok("stable binary path", p)
}

func (d *doctor) gitHook() {
	d.section("Git")
	val, present := readGlobalConfig("core.hooksPath")
	hooksDir, _ := GitHooksDirPath()
	if !present || val != hooksDir {
		d.bad("global core.hooksPath", fmt.Sprintf("expected %s, got %q", hooksDir, val),
			"`blamely install` to set it")
		return
	}
	d.ok("global core.hooksPath", val)
	// Confirm the post-commit hook file actually exists.
	hookFile := hooksDir + "/post-commit"
	if st, err := os.Stat(hookFile); err != nil {
		d.bad("post-commit script", fmt.Sprintf("missing (%s)", hookFile),
			"`blamely install` re-writes this file")
	} else if st.Mode()&0o111 == 0 {
		d.warn("post-commit script", fmt.Sprintf("%s not executable", hookFile),
			"chmod +x "+hookFile)
	} else {
		d.ok("post-commit script", hookFile)
	}
}

func (d *doctor) path() {
	d.section("PATH")
	want, err := InstalledBinaryPath()
	if err != nil {
		return
	}
	binDir := stripFile(want)
	sep := string(os.PathListSeparator)
	for _, p := range strings.Split(os.Getenv("PATH"), sep) {
		if strings.EqualFold(strings.TrimSpace(p), binDir) {
			d.ok("$PATH contains "+binDir, "")
			return
		}
	}
	fix := "open a new terminal"
	if runtime.GOOS != "windows" {
		fix = "restart your shell or `source ~/.zshrc` (the install added the entry)"
	} else {
		fix = "open a new terminal, or run: $env:Path = [Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [Environment]::GetEnvironmentVariable('Path','User')"
	}
	d.warn("$PATH", "does not contain "+binDir, fix)
}

func (d *doctor) db() {
	d.section("Database")
	p, err := config.DBPath()
	if err != nil {
		d.bad("db path", err.Error(), "")
		return
	}
	st, err := os.Stat(p)
	if err != nil {
		d.warn("db file", fmt.Sprintf("not yet created (%s)", p),
			"will be created on first daemon write — usually fine")
		return
	}
	d.ok("db file", fmt.Sprintf("%s (%s)", p, humanBytes(st.Size())))
}

// hookCheck describes one AI tool's hook file location and the marker string
// that proves blamely's command is wired up inside it.
type hookCheck struct {
	tool    string         // human label
	path    func() (string, error)
	marker  string         // substring proving the hook command is present
	requires bool          // true if the tool was detected — i.e. we should have installed it
}

func (d *doctor) hooks() {
	d.section("AI tool hooks")
	det, _ := Detect()
	checks := []hookCheck{
		// Markers omit the binary name: the hook command is `<path> record
		// <tool>`, and on Windows <path> ends in `blamely.exe`, so a "blamely
		// record <tool>" needle would never match. `record <tool>` is the
		// extension-agnostic tail that's always present.
		{"Claude (~/.claude/settings.json)", config.ClaudeSettingsPath, "record claude", det.Claude.Present},
		{"Cursor (~/.cursor/hooks.json)", config.CursorHooksPath, "record cursor", det.Cursor.Present},
		{"Codex (~/.codex/config.toml)", config.CodexConfigPath, "record codex", det.Codex.Present},
		{"Copilot (~/.copilot/hooks/blamely.json)", config.CopilotBlamelyHookPath, "record copilot", det.Copilot.Present},
		{"Gemini (~/.gemini/settings.json)", config.GeminiSettingsPath, "record gemini", det.Gemini.Present},
	}
	for _, c := range checks {
		p, err := c.path()
		if err != nil {
			d.bad(c.tool, err.Error(), "")
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			if !c.requires {
				d.ok(c.tool, "not detected, skipped")
				continue
			}
			d.bad(c.tool, fmt.Sprintf("file missing (%s)", p),
				"run `blamely repair` (will create it)")
			continue
		}
		if !strings.Contains(string(data), c.marker) {
			if !c.requires {
				d.ok(c.tool, "no blamely hook (tool not detected)")
				continue
			}
			d.bad(c.tool, "blamely hook NOT present in file",
				"run `blamely repair` to configure it")
			continue
		}
		d.ok(c.tool, p)
	}
}

func (d *doctor) summary() {
	fmt.Fprintln(d.w)
	if len(d.problems) == 0 {
		fmt.Fprintln(d.w, "✓ All checks passed. Blamely is healthy.")
		return
	}
	fmt.Fprintf(d.w, "✗ Found %d problem(s). Recommended fixes:\n", len(d.problems))
	for _, p := range d.problems {
		fmt.Fprintln(d.w, p)
	}
}

func stripFile(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[:i]
		}
	}
	return ""
}

func humanBytes(n int64) string {
	const k = 1024
	switch {
	case n < k:
		return fmt.Sprintf("%dB", n)
	case n < k*k:
		return fmt.Sprintf("%.1fKB", float64(n)/k)
	case n < k*k*k:
		return fmt.Sprintf("%.1fMB", float64(n)/(k*k))
	default:
		return fmt.Sprintf("%.1fGB", float64(n)/(k*k*k))
	}
}
