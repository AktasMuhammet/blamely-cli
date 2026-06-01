package tools

// Debug tracers for `blamely log <tool>`. They run a tool's real watcher(s)
// against a printing sink instead of the SQLite store, so users can watch — in
// real time — exactly which attribution events blamely would record. This
// reuses the production detection logic verbatim (no parallel parser to drift),
// and writes nothing to the database.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

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
					src = "plugin"
				}
				fmt.Fprintf(out, "%s  [%-10s] tool=%-7s model=%-22s conf=%-6s src=%-22s file=%s%s\n",
					ts.Format("15:04:05"), r.GenType, r.Tool, model, r.Confidence, src, file, lineRange)
			}
		}
	}
}

// DebugCodexLogs traces Codex CLI session detection via the CodexWatcher.
func DebugCodexLogs(ctx context.Context, out io.Writer) error {
	return DebugWatchers(ctx, out, &CodexWatcher{})
}

// DebugClaudeLogs explains that Claude Code attribution is hook-driven and has
// no passive log to tail, then traces the chat-session watcher in case Claude
// is being used through a chat panel that persists a chatSessions JSONL.
func DebugClaudeLogs(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "Claude Code attribution is hook-driven: the PostToolUse hook pipes each")
	fmt.Fprintln(out, "edit to `blamely record claude`, which POSTs directly to the daemon's /edit")
	fmt.Fprintln(out, "endpoint — there is no passive log file to tail. To trace it, run the")
	fmt.Fprintln(out, "daemon with BLAMELY_DEBUG and watch its stderr, or inspect the git note")
	fmt.Fprintln(out, "after a commit. Nothing to stream here.")
	return nil
}
