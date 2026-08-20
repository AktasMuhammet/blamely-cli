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
	"github.com/blamely/blamely/internal/updatehint"
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
	// Called from here, not inside binary(), so an available update is still
	// reported when the binary check itself bails out early.
	d.updateHint()
	d.gitHook()
	d.path()
	d.db()
	d.hooks()
	d.editors()
	d.summary()
	return nil
}

type doctor struct {
	w        io.Writer
	problems []string // human-readable list of failures, used in the summary
	notes    int      // `!` lines that are configuration, not faults (see note)
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

// note prints a visible `!` line WITHOUT counting it as a problem, for a state
// that is worth surfacing but is a legitimate configuration rather than a fault —
// a CLI-only install that deliberately skipped the IDE plugins, say. The action to
// take belongs in `detail`, since nothing is added to the summary's fix list.
//
// The distinction matters both ways: counting these would make doctor report
// problems on a perfectly good `--skip-plugins` install and train people to ignore
// it, while printing nothing at all is how a plugin-less machine came to read as
// "All checks passed".
func (d *doctor) note(label, detail string) {
	fmt.Fprintf(d.w, "  ! %-32s %s\n", label, detail)
	d.notes++
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
	// Windows has no Unix execute bit — Mode()&0o111 is always 0 for regular
	// files there, so this check (and its `chmod +x` advice) is meaningless and
	// would always false-positive. Existence is all that matters on Windows.
	if runtime.GOOS != "windows" && st.Mode()&0o111 == 0 {
		d.warn("stable binary path", fmt.Sprintf("%s (not executable)", p),
			"chmod +x "+p)
		return
	}
	d.ok("stable binary path", p)
}

// updateHint surfaces what the daemon's periodic check last found. It reads the
// recorded hint only — doctor never makes a network call of its own, so it stays
// instant and works offline.
func (d *doctor) updateHint() {
	h, ok := updatehint.Read()
	if !ok {
		return
	}
	d.warn("version", fmt.Sprintf("%s installed, %s available", Version, h.Version),
		"run `blamely update`")
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
	} else if runtime.GOOS != "windows" && st.Mode()&0o111 == 0 {
		// See binary(): no Unix execute bit on Windows, so skip this check there.
		// Git for Windows runs hooks via its bundled sh regardless of the bit.
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
	tool     string // human label
	path     func() (string, error)
	paths    func() []string // union of config locations (default + custom); wins over path when set
	marker   string          // substring proving the hook command is present
	requires bool            // true if the tool was detected — i.e. we should have installed it
}

func (d *doctor) hooks() {
	d.section("AI tool hooks")
	det, _ := Detect()
	checks := []hookCheck{
		// Markers omit the binary name: the hook command is `<path> record
		// <tool>`, and on Windows <path> ends in `blamely.exe`, so a "blamely
		// record <tool>" needle would never match. `record <tool>` is the
		// extension-agnostic tail that's always present.
		{tool: "Claude (~/.claude/settings.json + custom)", paths: config.ClaudeSettingsPaths, marker: "record claude", requires: det.Claude.Present},
		{tool: "Cursor (~/.cursor/hooks.json)", path: config.CursorHooksPath, marker: "record cursor", requires: det.Cursor.Present},
		{tool: "Codex (~/.codex/config.toml + custom)", paths: config.CodexConfigPaths, marker: "record codex", requires: det.Codex.Present},
		{tool: "Copilot (~/.copilot/hooks/blamely.json)", path: config.CopilotBlamelyHookPath, marker: "record copilot", requires: det.Copilot.Present},
		{tool: "Gemini (~/.gemini/settings.json)", path: config.GeminiSettingsPath, marker: "record gemini", requires: det.Gemini.Present},
	}
	for _, c := range checks {
		// Resolve the location(s) to check. `paths` (union of default + custom) wins
		// over the single `path` for tools that support a custom home (Claude/Codex).
		var locs []string
		if c.paths != nil {
			locs = c.paths()
		} else {
			p, err := c.path()
			if err != nil {
				d.bad(c.tool, err.Error(), "")
				continue
			}
			locs = []string{p}
		}

		// OK if the marker is present in ANY location; report the first match.
		matched := ""
		anyFile := false
		for _, p := range locs {
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			anyFile = true
			if strings.Contains(string(data), c.marker) {
				matched = p
				break
			}
		}
		if matched != "" {
			d.ok(c.tool, matched)
			continue
		}
		if !anyFile {
			if !c.requires {
				d.ok(c.tool, "not detected, skipped")
				continue
			}
			d.bad(c.tool, fmt.Sprintf("file missing (%s)", strings.Join(locs, ", ")),
				"run `blamely repair` (will create it)")
			continue
		}
		if !c.requires {
			d.ok(c.tool, "no blamely hook (tool not detected)")
			continue
		}
		d.bad(c.tool, "blamely hook NOT present in file",
			"run `blamely repair` to configure it")
	}
}

// editors checks the marketplace-distributed IDE plugins — the surface that gives
// a VS Code-family editor or a JetBrains IDE its own attribution UI (gutter,
// sidebar, chat-panel detection).
//
// Without this, doctor could report "All checks passed" on a machine where the
// editor plugin was never installed: every other check covers the CLI, the hooks
// and the daemon, none of which notice a missing plugin. That is a real
// false-clean — it is how a plugin-less install looked healthy while the user saw
// nothing in their editor.
//
// A missing plugin is reported with `note`, not `bad`: plenty of installs are
// CLI-only on purpose (`--skip-plugins`, a sideloaded dev build, a policy that
// forbids marketplace installs), so it is surfaced without being counted as a
// problem — visible enough to answer "why do I see nothing in my editor", quiet
// enough not to fail a deliberate CLI-only setup.
func (d *doctor) editors() {
	d.section("Editor plugins")

	for _, t := range editorExtensionTargets {
		cliPath, ok := findEditorCLI(t)
		if !ok {
			d.ok(t.Label, "not detected, skipped")
			continue
		}
		// Costs one CLI spawn per editor (`--list-extensions`). doctor is
		// user-invoked and this is the only way to ask the editor what it has, so
		// the second or so is worth an answer that is actually true.
		if extensionInstalled(cliPath, blamelyExtensionID) {
			d.ok(t.Label, blamelyExtensionID+" installed")
			continue
		}
		d.note(t.Label, "editor found, "+blamelyExtensionID+" NOT installed — run `blamely install`")
	}

	ides, err := findJetBrainsIDEs()
	if err != nil || len(ides) == 0 {
		d.ok("JetBrains IDEs", "not detected, skipped")
		return
	}
	for _, ide := range ides {
		if hasJetBrainsPlugin(ide.PluginsDir) {
			d.ok(ide.Label, "plugin installed · "+ide.PluginsDir)
			continue
		}
		d.note(ide.Label, "IDE found, Blamely plugin NOT installed — run `blamely install`, then restart the IDE")
	}
}

func (d *doctor) summary() {
	fmt.Fprintln(d.w)
	if len(d.problems) == 0 {
		// Acknowledge the `!` lines: "All checks passed" printed directly under one
		// reads as a contradiction, and that contradiction is exactly how a machine
		// with no editor plugin came across as fully healthy.
		if d.notes > 0 {
			fmt.Fprintf(d.w, "✓ No faults found. Blamely is healthy — see the %d note(s) marked ! above.\n", d.notes)
			return
		}
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
