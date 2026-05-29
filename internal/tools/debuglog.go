package tools

// Debug tracers for `blamely log <tool>`. They run a tool's real watcher(s)
// against a printing sink instead of the SQLite store, so users can watch — in
// real time — exactly which attribution events blamely would record. This
// reuses the production detection logic verbatim (no parallel parser to drift),
// and writes nothing to the database.

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/blamely/blamely/internal/daemon"
)

// printSink implements daemon.Sink by printing each event to out instead of
// persisting it. Session markers (no file path) are shown too, since their
// model / gen_type is exactly what feeds the chat-vs-completion enrichment.
type printSink struct {
	mu  sync.Mutex
	out io.Writer
}

func (p *printSink) Record(ev daemon.Event) error {
	loc := ev.FilePath
	if loc == "" {
		loc = "(session marker — no file)"
	}
	model := ev.Model
	if model == "" {
		model = "?"
	}
	gen := ev.GenType
	if gen == "" {
		gen = "unknown"
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Fprintf(p.out, "[%-10s] tool=%-7s model=%-22s conf=%-6s %s\n",
		gen, ev.Tool, model, ev.Confidence, loc)
	return nil
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
func DebugCopilotLogs(ctx context.Context, out io.Writer) error {
	return DebugWatchers(ctx, out, &CopilotChatWatcher{}, &CopilotLogWatcher{})
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
