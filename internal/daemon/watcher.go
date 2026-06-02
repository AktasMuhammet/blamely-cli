package daemon

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/blamely/blamely/internal/store"
)

// Watcher is a long-running, cancellable component that pushes attribution
// events. Each AI tool (Codex, Cursor, Copilot) implements this interface.
//
// Run MUST honor ctx.Done() and return promptly. It should be safe to start
// multiple Watchers in parallel under the same daemon process.
type Watcher interface {
	Name() string
	Run(ctx context.Context, sink Sink) error
}

// Sink is what a Watcher calls when it observes an AI-tagged edit.
// The daemon implementation writes through to the SQLite store.
type Sink interface {
	Record(ev Event) error
}

// Event is the normalized payload every Watcher produces. It is shaped like
// EditPayload but with parsed-out tokens and an explicit timestamp so a
// historical replay (e.g. parsing yesterday's session log on daemon startup)
// keeps the original timing.
type Event struct {
	When             time.Time
	Tool             string // claude|cursor|codex|copilot
	Confidence       string // high|medium|low — defaulted from Tool if blank
	// GenType describes how the edit was produced.
	// Values: chat | cli | completion | unknown
	GenType          string
	RepoPath         string
	FilePath         string // relative to repo root
	Model            string
	InputTokens      *int64
	OutputTokens     *int64
	CacheReadTokens  *int64
	CacheWriteTokens *int64
	HashBefore       string
	HashAfter        string
	RawMeta          string
	// SuggestedLines is the AI's original suggestion size at the moment the
	// watcher observed the event, before any partial-acceptance/user-editing.
	SuggestedLines int64
	Lines          []LineRange
	// Branch is the checked-out branch for this edit. Watchers usually leave it
	// empty; the sink resolves it from RepoPath. Editor-pushed events may set it.
	Branch string
}

type LineRange struct {
	Start      int
	End        int
	ContentSHA string
}

// dbSink writes Events directly through to the SQLite store.
type dbSink struct {
	db *store.DB
}

func (s *dbSink) Record(ev Event) error {
	tool := store.Tool(ev.Tool)
	gt := store.GenType(ev.GenType)
	if gt == "" {
		gt = store.GenTypeUnknown
	}
	// Empty tool is only legitimate for human-typed code, which the
	// HumanEditWatcher emits as tool="" + gen_type=human. Anything else
	// with an empty tool is a watcher bug.
	if tool == "" && gt != store.GenTypeHuman {
		return fmt.Errorf("watcher sink: tool required (or use gen_type=human for empty tool)")
	}
	switch tool {
	case "",
		store.ToolClaude, store.ToolCursor, store.ToolCodex, store.ToolCopilot, store.ToolCopyPaste,
		store.ToolHuman: // accepted for legacy in-flight events; new emissions should use ""
	default:
		return fmt.Errorf("watcher sink: unknown tool %q", ev.Tool)
	}
	conf := store.Confidence(ev.Confidence)
	if conf == "" {
		conf = defaultConfidence(tool)
	}
	ts := ev.When
	if ts.IsZero() {
		ts = time.Now()
	}
	e := store.Edit{
		TimestampNanos: ts.UnixNano(),
		RepoPath:       ev.RepoPath,
		FilePath:       ev.FilePath,
		Tool:           tool,
		Confidence:     conf,
		GenType:        gt,
		SuggestedLines: ev.SuggestedLines,
	}
	if ev.Model != "" {
		e.Model = sql.NullString{Valid: true, String: ev.Model}
	}
	setNullInt(&e.InputTokens, ev.InputTokens)
	setNullInt(&e.OutputTokens, ev.OutputTokens)
	setNullInt(&e.CacheReadTokens, ev.CacheReadTokens)
	setNullInt(&e.CacheWriteTokens, ev.CacheWriteTokens)
	if ev.HashBefore != "" {
		e.HashBefore = sql.NullString{Valid: true, String: ev.HashBefore}
	}
	if ev.HashAfter != "" {
		e.HashAfter = sql.NullString{Valid: true, String: ev.HashAfter}
	}
	if ev.RawMeta != "" {
		e.RawMeta = sql.NullString{Valid: true, String: ev.RawMeta}
	}
	// Same chat enrichment as the HTTP path: upgrade gen_type + backfill model
	// from recent chat-session markers for copilot/cursor edits arriving via a
	// log/velocity watcher (which can't tell a chat apply from a Tab accept).
	enrichChatEdit(s.db, &e)
	for _, r := range ev.Lines {
		if r.Start <= 0 || r.End < r.Start {
			continue
		}
		e.Lines = append(e.Lines, store.EditLine{StartLine: r.Start, EndLine: r.End, ContentSHA: r.ContentSHA})
	}
	sessions.resolve(s.db, &e, ev.Branch)
	if _, err := s.db.InsertEdit(e); err != nil {
		return err
	}
	// When a chat-session marker lands, retroactively re-stamp the apply edit
	// that the editor plugin already recorded as a completion a beat earlier
	// (the chat response streams in slightly after the apply hits the file).
	if e.GenType == store.GenTypeChat && (e.Tool == store.ToolCopilot || e.Tool == store.ToolCursor) {
		if err := s.db.UpgradeRecentCompletionsToChat(e.Tool, e.TimestampNanos, chatEnrichWindowNanos); err != nil {
			log.Printf("watcher sink: upgrade completions: %v", err)
		}
	}
	return nil
}

// runWatchers fans the configured watchers out as goroutines. Errors are
// logged but never bring the daemon down — a broken tailer for one tool
// shouldn't take out attribution for the others.
func runWatchers(ctx context.Context, db *store.DB, watchers []Watcher) {
	if len(watchers) == 0 {
		return
	}
	sink := &dbSink{db: db}
	var wg sync.WaitGroup
	for _, w := range watchers {
		w := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			log.Printf("watcher started: %s", w.Name())
			if err := w.Run(ctx, sink); err != nil && ctx.Err() == nil {
				log.Printf("watcher %s exited: %v", w.Name(), err)
			}
		}()
	}
	<-ctx.Done()
	wg.Wait()
}
