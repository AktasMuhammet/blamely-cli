package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blamely/blamely/internal/daemon"
	"github.com/blamely/blamely/internal/gitutil"
	"github.com/blamely/blamely/internal/store"
)

// CopilotTranscriptWatcher tails the GitHub Copilot Chat extension's own
// transcript event stream:
//
//	<workspaceStorage>/<hash>/GitHub.copilot-chat/transcripts/<id>.jsonl
//
// Unlike VS Code's built-in chatSessions store (which VS Code flushes lazily —
// sometimes minutes after the edit, so a quick commit attributes the AI's work
// to Human), the extension APPENDS to this transcript in real time as the chat
// streams. Byte-offset tailing it lets blamely record Copilot Chat edits within
// seconds, eliminating the commit-races-the-watcher lag.
//
// Edits arrive as apply_patch tool calls in the codex `*** Begin Patch` format;
// we parse them per line (content_sha-based, like every other blamely source) so
// attribution survives line drift. Tokens/model come from the surrounding
// assistant.message / tool.execution_complete events.
type CopilotTranscriptWatcher struct {
	// Roots overrides the workspaceStorage scan roots for tests.
	Roots []string
	DB    *store.DB
}

func (c *CopilotTranscriptWatcher) Name() string { return "copilot-transcript" }

// copilotTranscriptStaleCutoff bounds discovery to recently-touched transcripts
// (shares BLAMELY_CHAT_STALE_HOURS with the chat watcher).
var copilotTranscriptStaleCutoff = copilotChatStaleCutoff

func (c *CopilotTranscriptWatcher) Run(ctx context.Context, sink daemon.Sink) error {
	roots := c.Roots
	if len(roots) == 0 {
		roots = defaultCopilotChatRoots()
	}

	tailers := map[string]context.CancelFunc{}
	defer func() {
		for _, cancel := range tailers {
			cancel()
		}
	}()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		seen := map[string]bool{}
		for _, root := range roots {
			for _, path := range findCopilotTranscriptFiles(root) {
				seen[path] = true
				if _, running := tailers[path]; running {
					continue
				}
				tCtx, cancel := context.WithCancel(ctx)
				tailers[path] = cancel
				go func(p string) {
					if err := tailCopilotTranscript(tCtx, p, sink, c.DB, c.Name()); err != nil && tCtx.Err() == nil {
						log.Printf("copilot-transcript tail %s: %v", p, err)
					}
				}(path)
			}
		}
		for path, cancel := range tailers {
			if !seen[path] {
				cancel()
				delete(tailers, path)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// findCopilotTranscriptFiles returns every recent
// <root>/<hash>/GitHub.copilot-chat/transcripts/*.jsonl.
func findCopilotTranscriptFiles(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name(), "GitHub.copilot-chat", "transcripts")
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			p := filepath.Join(dir, f.Name())
			if info, ierr := f.Info(); ierr == nil && time.Since(info.ModTime()) > copilotTranscriptStaleCutoff {
				continue
			}
			out = append(out, p)
		}
	}
	return out
}

// copilotTranscriptEvent is the tolerant view of one transcript line.
type copilotTranscriptEvent struct {
	Type string `json:"type"`
	Data struct {
		ModelID      string `json:"modelId"`
		Model        string `json:"model"`
		OutputTokens int64  `json:"outputTokens"`
		ToolRequests []struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"toolRequests"`
	} `json:"data"`
}

func tailCopilotTranscript(ctx context.Context, path string, sink daemon.Sink, db *store.DB, watcherName string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	var startOffset int64
	resumed := false
	if db != nil {
		if wm, ok := db.LoadWatermark(watcherName, path); ok {
			if fi, e := f.Stat(); e == nil && wm.ByteOffset <= fi.Size() {
				if _, e := f.Seek(wm.ByteOffset, io.SeekStart); e == nil {
					startOffset = wm.ByteOffset
					resumed = true
				}
			}
		}
	}
	if !resumed {
		// Prime to end on first sight: don't replay a transcript's history (those
		// edits were already attributed via chatSessions / past commits). We only
		// want NEW appends as the chat streams live — that's the whole point of
		// reading this real-time stream.
		if end, e := f.Seek(0, io.SeekEnd); e == nil {
			startOffset = end
		}
	}
	reader := bufio.NewReaderSize(f, 1<<16)
	offset := startOffset
	savedOffset := startOffset
	var lastSave time.Time // zero → first real save (on a new append) fires immediately
	saveWM := func(force bool) {
		if db == nil || offset == savedOffset {
			return
		}
		if !force && time.Since(lastSave) < 500*time.Millisecond {
			return
		}
		var size, mt int64
		if fi, e := f.Stat(); e == nil {
			size, mt = fi.Size(), fi.ModTime().UnixNano()
		}
		if err := db.SaveWatermark(watcherName, path, store.Watermark{ByteOffset: offset, Size: size, MtimeNanos: mt}); err == nil {
			savedOffset, lastSave = offset, time.Now()
		}
	}
	defer saveWM(true)

	model := ""
	for {
		line, rerr := readLineGrowing(ctx, reader, f)
		if rerr != nil {
			if rerr == io.EOF || ctx.Err() != nil {
				return nil
			}
			return rerr
		}
		if pos, e := f.Seek(0, io.SeekCurrent); e == nil {
			offset = pos - int64(reader.Buffered())
		}
		if len(line) == 0 || line[0] != '{' {
			saveWM(false)
			continue
		}
		var ev copilotTranscriptEvent
		if json.Unmarshal(line, &ev) != nil {
			saveWM(false)
			continue
		}
		switch ev.Type {
		case "tool.execution_complete":
			if ev.Data.Model != "" {
				model = displayModel(ev.Data.Model)
			}
		case "assistant.message":
			if ev.Data.ModelID != "" {
				model = displayModel(ev.Data.ModelID)
			}
			hasPatch := false
			for _, tr := range ev.Data.ToolRequests {
				if looksLikePatch(tr.Name) {
					hasPatch = true
					break
				}
			}
			if !hasPatch {
				saveWM(false)
				continue
			}
			// The transcript carries the edits but NOT the model or token counts —
			// those live in the sibling chatSessions file. Read them on demand so
			// real-time Copilot Chat edits still report model + tokens.
			m, inTok, outTok := chatSessionModelUsage(path)
			if m == "" {
				m = model
			}
			for _, tr := range ev.Data.ToolRequests {
				if !looksLikePatch(tr.Name) {
					continue // chat only — read_file/manage_todo_list/etc. and inline completions are never recorded here
				}
				emitCopilotPatchEdits(tr.Arguments, m, inTok, outTok, path, sink)
			}
		}
		saveWM(false)
	}
}

// emitCopilotPatchEdits parses an apply_patch tool argument (codex `*** Begin
// Patch` body, possibly double-encoded as a JSON string) and emits one chat edit
// per touched file, with per-line added content_shas and removed-line hashes.
func emitCopilotPatchEdits(args json.RawMessage, model string, inputTokens, outputTokens int64, sessionPath string, sink daemon.Sink) {
	// arguments may be a JSON string literal wrapping the real JSON.
	raw := args
	var asStr string
	if json.Unmarshal(args, &asStr) == nil && strings.TrimSpace(asStr) != "" {
		raw = json.RawMessage(asStr)
	}
	body, _ := patchEnvelope(raw)
	if body == "" {
		return
	}
	for _, fe := range parseApplyPatchPerLine(body) {
		if fe.repoPath == "" || (len(fe.added) == 0 && len(fe.removed) == 0) {
			continue
		}
		ev := daemon.Event{
			When:         time.Now(),
			Tool:         "copilot",
			Confidence:   "high",
			GenType:      "chat",
			Model:        model,
			RepoPath:     fe.repoPath,
			FilePath:     fe.rel,
			Lines:        fe.added,
			RemovedLines: fe.removed,
			RawMeta:      fmt.Sprintf(`{"source":"copilot_transcript","transcript_path":%q}`, sessionPath),
		}
		if inputTokens > 0 {
			v := inputTokens
			ev.InputTokens = &v
		}
		if outputTokens > 0 {
			v := outputTokens
			ev.OutputTokens = &v
		}
		if err := sink.Record(ev); err != nil {
			log.Printf("copilot-transcript sink: %v", err)
		} else {
			log.Printf("copilot-transcript: apply %s +%d -%d model=%s", fe.rel, len(fe.added), len(fe.removed), model)
		}
	}
}

// chatSessionModelUsage reads the model + latest token usage from the chatSessions
// file that pairs with a transcript (same session id, sibling directory). The
// transcript stream itself has neither, so this is the only source. Best-effort:
// returns zero values if the chatSession isn't written yet (it lags), in which
// case the edit still records in real time, just without tokens.
func chatSessionModelUsage(transcriptPath string) (model string, inputTokens, outputTokens int64) {
	// .../<hash>/GitHub.copilot-chat/transcripts/<id>.jsonl
	//   → .../<hash>/chatSessions/<id>.jsonl
	id := filepath.Base(transcriptPath)
	hashDir := filepath.Dir(filepath.Dir(filepath.Dir(transcriptPath)))
	cs, err := parseChatSession(filepath.Join(hashDir, "chatSessions", id))
	if err != nil || cs == nil {
		return "", 0, 0
	}
	model = cs.model
	for i := len(cs.requests) - 1; i >= 0; i-- {
		r := cs.requests[i]
		if r.promptTokens > 0 || r.completionTokens > 0 {
			return model, r.promptTokens, r.completionTokens
		}
	}
	return model, 0, 0
}

type copilotPatchFile struct {
	repoPath string
	rel      string
	added    []daemon.LineRange
	removed  []daemon.RemovedLineHash
}

// parseApplyPatchPerLine parses a codex `*** Begin Patch` body into per-file,
// per-line added ranges (content_sha + norm) and removed-line hashes. Positions
// are sequential placeholders — commit-time attribution matches by content_sha,
// not line number, so the AI lines survive drift.
func parseApplyPatchPerLine(body string) []copilotPatchFile {
	var out []copilotPatchFile
	var cur *copilotPatchFile
	flush := func() {
		if cur != nil && (len(cur.added) > 0 || len(cur.removed) > 0) {
			out = append(out, *cur)
		}
		cur = nil
	}
	setFile := func(p string) {
		flush()
		abs := strings.TrimSpace(p)
		if r, err := filepath.EvalSymlinks(abs); err == nil {
			abs = r
		}
		repo, _ := gitutil.RepoID(abs)
		rel := abs
		if wt, _ := gitutil.Toplevel(abs); wt != "" {
			if r, err := filepath.Rel(wt, abs); err == nil && !strings.HasPrefix(r, "..") {
				rel = r
			}
		}
		cur = &copilotPatchFile{repoPath: repo, rel: rel}
	}
	addN := 0
	for _, line := range strings.Split(body, "\n") {
		switch {
		case strings.HasPrefix(line, "*** Update File: "):
			setFile(strings.TrimPrefix(line, "*** Update File: "))
		case strings.HasPrefix(line, "*** Add File: "):
			setFile(strings.TrimPrefix(line, "*** Add File: "))
		case strings.HasPrefix(line, "*** Delete File: "):
			setFile(strings.TrimPrefix(line, "*** Delete File: "))
		case strings.HasPrefix(line, "*** End Patch"):
			flush()
		case cur == nil:
			// outside a file section
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			text := strings.TrimRight(strings.TrimPrefix(line, "+"), "\r")
			if strings.TrimSpace(text) == "" {
				continue
			}
			addN++
			cur.added = append(cur.added, daemon.LineRange{
				Start: addN, End: addN,
				ContentSHA:     sha256Hex([]byte(text)),
				ContentSHANorm: sha256HexNorm(text),
			})
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			text := strings.TrimRight(strings.TrimPrefix(line, "-"), "\r")
			if strings.TrimSpace(text) == "" {
				continue
			}
			cur.removed = append(cur.removed, daemon.RemovedLineHash{
				ContentSHA:     sha256Hex([]byte(text)),
				ContentSHANorm: sha256HexNorm(text),
			})
		}
	}
	flush()
	return out
}
