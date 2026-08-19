package tools

// Debug tracers for `blamely log <tool>`. They run a tool's real watcher(s)
// against a printing sink instead of the SQLite store, so users can watch — in
// real time — exactly which attribution events blamely would record. This
// reuses the production detection logic verbatim (no parallel parser to drift),
// and writes nothing to the database.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/blamely/blamely/internal/config"
	"github.com/blamely/blamely/internal/daemon"
	"github.com/blamely/blamely/internal/store"
)

// printSink implements daemon.Sink by printing each event to out instead of
// persisting it. Session markers (no file path) are shown too, since their
// model / gen_type is exactly what feeds the chat-vs-completion enrichment.
//
// The output is verbose on purpose — this is a debugging aid. For each event we
// surface the timestamp, gen_type, tool, model, confidence, the raw_meta source
// (which watcher/path produced it), the file (or "no file"), and — for chat /
// log signals tied to a workspace file — which editor (VS Code vs Cursor) the
// underlying artifact lives under. That last column is what makes setup issues
// obvious (e.g. "all my Copilot chat activity is under VS Code, not Cursor").
type printSink struct {
	mu    sync.Mutex
	out   io.Writer
	count int
}

// rawMetaView is the subset of raw_meta the debug printer understands.
type rawMetaView struct {
	Source          string `json:"source"`
	ChatSessionPath string `json:"chat_session_path"`
	Tool            string `json:"tool"`
	Host            string `json:"host"`
	Line            string `json:"line"`
}

func (p *printSink) Record(ev daemon.Event) error {
	var meta rawMetaView
	if ev.RawMeta != "" {
		_ = json.Unmarshal([]byte(ev.RawMeta), &meta)
	}

	model := ev.Model
	if model == "" {
		model = "?"
	}
	gen := ev.GenType
	if gen == "" {
		gen = "unknown"
	}
	source := meta.Source
	if source == "" {
		source = "?"
	}
	loc := ev.FilePath
	if loc == "" {
		loc = "—"
	}
	ts := ev.When
	if ts.IsZero() {
		ts = time.Now()
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.count++
	fmt.Fprintf(p.out, "%s  [%-10s] tool=%-7s model=%-22s conf=%-6s src=%-22s file=%s\n",
		ts.Format("15:04:05"), gen, ev.Tool, model, ev.Confidence, source, loc)
	// Secondary line: the artifact that produced this signal + which editor it
	// belongs to, so cross-editor confusion (Copilot-in-VS-Code vs Cursor) is
	// visible at a glance.
	if meta.ChatSessionPath != "" {
		fmt.Fprintf(p.out, "             ↳ chat session: %s  [editor=%s]\n",
			abbreviateHome(meta.ChatSessionPath), editorOfPath(meta.ChatSessionPath))
	}
	if meta.Host != "" {
		fmt.Fprintf(p.out, "             ↳ host: %s\n", meta.Host)
	}
	if meta.Line != "" {
		fmt.Fprintf(p.out, "             ↳ matched log line: %s\n", truncate(meta.Line, 160))
	}
	return nil
}

// editorOfPath guesses which editor a workspace artifact belongs to from its
// path. Returns "VS Code", "Cursor", or "?".
func editorOfPath(p string) string {
	switch {
	case strings.Contains(p, "/Code/") || strings.Contains(p, "\\Code\\"):
		return "VS Code"
	case strings.Contains(p, "/Cursor/") || strings.Contains(p, "\\Cursor\\"):
		return "Cursor"
	default:
		return "?"
	}
}

func abbreviateHome(p string) string {
	if h := homeDir(); h != "" && strings.HasPrefix(p, h) {
		return "~" + strings.TrimPrefix(p, h)
	}
	return p
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// DebugWatchers runs the given watchers against a printing sink until ctx is
// cancelled. It is the shared backend for the per-tool `blamely log` tracers.
func DebugWatchers(ctx context.Context, out io.Writer, watchers ...daemon.Watcher) error {
	if len(watchers) == 0 {
		return nil
	}
	names := make([]string, 0, len(watchers))
	for _, w := range watchers {
		names = append(names, w.Name())
	}
	fmt.Fprintf(out, "Tracing watchers: %v\n", names)
	fmt.Fprintf(out, "Printing detected attribution events (nothing is written to the DB). Ctrl-C to stop.\n\n")

	sink := &printSink{out: out}
	var wg sync.WaitGroup
	for _, w := range watchers {
		w := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := w.Run(ctx, sink); err != nil && ctx.Err() == nil {
				fmt.Fprintf(out, "[error] %s: %v\n", w.Name(), err)
			}
		}()
	}
	<-ctx.Done()
	wg.Wait()
	return nil
}

// DebugCopilotLogs traces GitHub Copilot detection: the chat-session JSONL
// watcher (chat panel, with model) and the editor / JetBrains log watcher
// (inline completion accepts + JetBrains fetch lines).
//
// It also polls SQLite every 2 s for new events from IDE plugin sources
// (intellij_plugin, vscode_plugin) that arrive via the HTTP /edit endpoint
// and are never seen by the watcher stream. Those rows are printed with the
// same format, prefixed with "↳ plugin" so they are visually distinct.
func DebugCopilotLogs(ctx context.Context, out io.Writer) error {
	go tailPluginEdits(ctx, out)
	return DebugWatchers(ctx, out, &CopilotChatWatcher{}, &CopilotLogWatcher{})
}

// tailPluginEdits polls SQLite every 2 s for new rows whose raw_meta source
// is an IDE plugin (intellij_plugin, vscode_plugin). These events come from
// the daemon's HTTP /edit endpoint and are never emitted to the watcher
// stream, so they would otherwise be invisible in `blamely log copilot`.
func tailPluginEdits(ctx context.Context, out io.Writer) {
	db, err := store.Open()
	if err != nil {
		return
	}
	defer db.Close()

	sources := []string{"intellij_plugin", "vscode_plugin"}
	var lastID int64
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rows, err := db.RecentPluginEdits(sources, lastID)
			if err != nil {
				continue
			}
			for _, r := range rows {
				if r.ID > lastID {
					lastID = r.ID
				}
				printPluginEditRow(out, r, "plugin")
			}
		}
	}
}

// printPluginEditRow renders one polled edits-table row in the same format
// printSink uses for watcher events, so HTTP-arriving and watcher-detected
// signals look identical in the trace output. fallbackSrc is shown when
// raw_meta carries no "source" field.
func printPluginEditRow(out io.Writer, r store.PluginEditRow, fallbackSrc string) {
	ts := time.Unix(0, r.Ts)
	model := r.Model
	if model == "" {
		model = "?"
	}
	file := r.FilePath
	if file == "" {
		file = "—"
	}
	lineRange := ""
	if r.StartLine > 0 {
		if r.EndLine > r.StartLine {
			lineRange = fmt.Sprintf(" L%d-%d", r.StartLine, r.EndLine)
		} else {
			lineRange = fmt.Sprintf(" L%d", r.StartLine)
		}
	}
	var meta struct {
		Source string `json:"source"`
	}
	_ = json.Unmarshal([]byte(r.RawMeta), &meta)
	src := meta.Source
	if src == "" {
		src = fallbackSrc
	}
	fmt.Fprintf(out, "%s  [%-10s] tool=%-7s model=%-22s conf=%-6s src=%-22s file=%s%s\n",
		ts.Format("15:04:05"), r.GenType, r.Tool, model, r.Confidence, src, file, lineRange)
}

// tailToolEdits polls SQLite every 2 s for new edits-table rows for the given
// tool and prints them as they're recorded. Used for hook-driven tools whose
// events arrive solely via the HTTP /edit endpoint — there's no passive log
// or watcher stream to trace, so the database itself is the live signal.
func tailToolEdits(ctx context.Context, out io.Writer, tool store.Tool, fallbackSrc string) {
	db, err := store.Open()
	if err != nil {
		return
	}
	defer db.Close()

	var lastID int64
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rows, err := db.RecentEditsByTool(tool, lastID)
			if err != nil {
				continue
			}
			for _, r := range rows {
				if r.ID > lastID {
					lastID = r.ID
				}
				printPluginEditRow(out, r, fallbackSrc)
			}
		}
	}
}

// DebugCodexLogs traces Codex CLI session detection via the CodexWatcher.
func DebugCodexLogs(ctx context.Context, out io.Writer) error {
	return DebugWatchers(ctx, out, &CodexWatcher{})
}

// DebugClaudeLogs traces Claude Code attribution. Claude is hook-driven: the
// PostToolUse hook pipes each edit to `blamely record claude`, which POSTs to
// the daemon's /edit endpoint — there is no passive log file a watcher could
// tail, so this command has two sources instead.
//
// Without --debug it follows the database: every new tool=claude row is printed
// the moment it is recorded. That shows what SUCCEEDED.
//
// With --debug it also backfills and follows ~/.blamely/claude-debug.log, the
// step-by-step trace each hook process writes (see claudedebug.go). That shows
// what the hook actually did — including the runs that recorded nothing, which
// are invisible in the database by definition and are what you are usually
// looking for. Nothing is written to the database by this command.
func DebugClaudeLogs(ctx context.Context, debug bool, out io.Writer) error {
	fmt.Fprintln(out, "Claude Code attribution is hook-driven: the PostToolUse hook pipes each")
	fmt.Fprintln(out, "edit to `blamely record claude`, which POSTs directly to the daemon's /edit")
	fmt.Fprintln(out, "endpoint — there is no passive log file to tail.")
	fmt.Fprintln(out)

	if !debug {
		fmt.Fprintln(out, "Following the database: every new tool=claude row is printed as it lands.")
		fmt.Fprintln(out, "Edit a file from Claude Code now and watch for it below. Re-run with")
		fmt.Fprintln(out, "--debug to also see the hook's own step-by-step trace, which covers the")
		fmt.Fprintln(out, "runs that recorded NOTHING (bad payload, no repo, daemon down).")
		fmt.Fprintln(out, "Ctrl-C to stop.")
		fmt.Fprintln(out)
		tailToolEdits(ctx, out, store.ToolClaude, "claude_hook")
		return nil
	}

	fmt.Fprintln(out, "--debug: showing the hook's own trace. Each `blamely record claude` run")
	fmt.Fprintln(out, "writes one block of lines sharing an invocation id, stepping through:")
	fmt.Fprintln(out, "  payload    raw stdin the hook received (truncated)")
	fmt.Fprintln(out, "  parse      tool_name / session / cwd / transcript_path, or the parse error")
	fmt.Fprintln(out, "  transcript transcript path derived when the payload omitted it")
	fmt.Fprintln(out, "  extract    the file and line ranges pulled out of tool_input")
	fmt.Fprintln(out, "  branch     why a payload produced no file edit (Bash scan, no path, …)")
	fmt.Fprintln(out, "  resolve    repo / worktree / repo-relative path")
	fmt.Fprintln(out, "  gentype    chat vs cli vs completion, and where it came from")
	fmt.Fprintln(out, "  usage      model and token counts read from the transcript")
	fmt.Fprintln(out, "  post       the daemon endpoint and whether the POST succeeded")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "If an edit is missing, read its block top-down — the first step that")
	fmt.Fprintln(out, "reports empty/UNREACHABLE/REJECTED is the cause. Set BLAMELY_DEBUG_CLAUDE=0")
	fmt.Fprintln(out, "to turn this logging off.")
	fmt.Fprintln(out)

	// Database rows in parallel: the hook log says what the hook sent, the DB
	// says what the daemon kept. Seeing both at once is what separates "the
	// hook never fired" from "the hook fired and the daemon dropped it".
	go tailToolEdits(ctx, out, store.ToolClaude, "claude_hook")
	tailClaudeDebugLog(ctx, out, claudeDebugBackfillLines)
	return nil
}

// DebugGeminiLogs traces Gemini CLI attribution. Gemini is hook-driven — the
// AfterTool/BeforeTool hooks pipe each tool call straight to `blamely record
// gemini`, which POSTs to the daemon's /edit endpoint — so there's no passive
// log file to tail. The edits table itself is the only live signal: we poll
// it for new tool=gemini rows and print them as they land, in the same format
// `blamely log copilot` uses for its plugin events.
func DebugGeminiLogs(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "Gemini CLI attribution is hook-driven: the AfterTool/BeforeTool hooks pipe")
	fmt.Fprintln(out, "each tool call to `blamely record gemini`, which POSTs directly to the")
	fmt.Fprintln(out, "daemon's /edit endpoint — there is no passive log file to tail, so this")
	fmt.Fprintln(out, "traces the database directly: every new tool=gemini row is printed the")
	fmt.Fprintln(out, "moment it's recorded. Trigger an edit in Gemini CLI now and watch for it")
	fmt.Fprintln(out, "below. Nothing is written to the database by this command. Ctrl-C to stop.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "If nothing appears after you edit a file via Gemini, check:")
	fmt.Fprintln(out, "  1. ~/.gemini/settings.json has tools.enableHooks=true and an")
	fmt.Fprintln(out, "     AfterTool/BeforeTool hook running `blamely record gemini` (run")
	fmt.Fprintln(out, "     `blamely doctor` or `blamely install` to (re)write it).")
	fmt.Fprintln(out, "  2. The daemon is running (`blamely status`).")
	fmt.Fprintln(out, "  3. `echo '<hook payload>' | blamely record gemini` prints no error.")
	fmt.Fprintln(out, "     A rejection (e.g. \"daemon rejected: 400 ...\") prints directly to")
	fmt.Fprintln(out, "     this command's own output — the daemon does NOT log rejected or")
	fmt.Fprintln(out, "     malformed POSTs to ~/.blamely/daemon.log, so check here first.")
	fmt.Fprintln(out)

	tailToolEdits(ctx, out, store.ToolGemini, "gemini_hook")
	return nil
}

// homeDir returns the user's home directory (best-effort).
func homeDir() string {
	h, _ := config.Home()
	return h
}

// claudeDebugBackfillLines is how much of the existing hook log
// `blamely log claude --debug` prints before it starts following. The point of
// the flag is to inspect requests that ALREADY happened, so the backfill is the
// primary output, not a courtesy.
const claudeDebugBackfillLines = 300

// tailClaudeDebugLog backfills the tail of ~/.blamely/claude-debug.log and then
// follows it until ctx is cancelled. The file is written by short-lived hook
// processes, so it may not exist yet, may appear mid-follow, and may be rotated
// out from under us — all three are handled by re-opening on stat mismatch.
func tailClaudeDebugLog(ctx context.Context, out io.Writer, backfill int) {
	path, err := ClaudeDebugLogPath()
	if err != nil {
		fmt.Fprintf(out, "[error] cannot resolve the hook log path: %v\n", err)
		return
	}
	fmt.Fprintf(out, "Hook log: %s\n", abbreviateHome(path))
	if _, err := os.Stat(path); err != nil {
		fmt.Fprintf(out, "  (not created yet — it appears the first time Claude edits a file)\n")
	}
	fmt.Fprintln(out)

	// Backfill from the rotated generation first, so a log that has just rolled
	// over still shows the requests that preceded the roll.
	lines := lastLinesOfFiles(backfill, path+claudeDebugRotatedSuffix, path)
	if len(lines) > 0 {
		fmt.Fprintf(out, "── last %d hook log line(s) ──\n", len(lines))
		for _, l := range lines {
			fmt.Fprintln(out, l)
		}
		fmt.Fprintf(out, "── following live ──\n")
	}

	followFile(ctx, path, out)
}

// lastLinesOfFiles returns up to n trailing lines across the given files, read
// in order (oldest generation first).
func lastLinesOfFiles(n int, paths ...string) []string {
	var all []string
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, l := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			if l != "" {
				all = append(all, l)
			}
		}
	}
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return all
}

// followFile prints lines appended to path from now on. It polls rather than
// using fsnotify because the writers are separate hook processes and the file
// is recreated on rotation: a stat-based poll handles create, append, truncate
// and rename with one mechanism.
func followFile(ctx context.Context, path string, out io.Writer) {
	var (
		f      *os.File
		reader *bufio.Reader
		offset int64
	)
	defer func() {
		if f != nil {
			f.Close()
		}
	}()

	// Start at the current end so the follow phase shows only new lines; the
	// backfill above already covered the history.
	if fi, err := os.Stat(path); err == nil {
		offset = fi.Size()
	}

	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		fi, err := os.Stat(path)
		if err != nil {
			// Rotated away or not created yet; drop the handle and retry.
			if f != nil {
				f.Close()
				f, reader = nil, nil
			}
			offset = 0
			continue
		}
		// Truncated or replaced by rotation: restart from the beginning so the
		// new generation's first lines aren't skipped.
		if fi.Size() < offset {
			if f != nil {
				f.Close()
				f, reader = nil, nil
			}
			offset = 0
		}
		if f == nil {
			nf, err := os.Open(path)
			if err != nil {
				continue
			}
			if _, err := nf.Seek(offset, io.SeekStart); err != nil {
				nf.Close()
				continue
			}
			f, reader = nf, bufio.NewReaderSize(nf, 1<<16)
		}
		for {
			line, err := reader.ReadString('\n')
			offset += int64(len(line))
			if len(line) > 0 && strings.HasSuffix(line, "\n") {
				fmt.Fprint(out, line)
				continue
			}
			// Partial line (a hook is mid-write) or EOF: rewind the partial read
			// so the next poll re-reads it whole.
			offset -= int64(len(line))
			if _, serr := f.Seek(offset, io.SeekStart); serr == nil {
				reader.Reset(f)
			}
			if err != nil && !errors.Is(err, io.EOF) {
				fmt.Fprintf(out, "[error] read %s: %v\n", abbreviateHome(path), err)
			}
			break
		}
	}
}
