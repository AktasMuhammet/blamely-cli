package tools

// CopilotChatWatcher reads VS Code chat-session JSONL files and attributes them
// to GitHub Copilot. It only watches VS Code's workspaceStorage directory.
//
// Cursor's chat sessions are handled by CursorChatWatcher (cursor_chat.go),
// which watches Cursor's workspaceStorage directory and always emits tool=cursor.
// The two watchers are fully independent — changing one has no effect on the other.
//
// File layout (per inspection — undocumented, subject to change):
//
//	~/Library/Application Support/Code/User/workspaceStorage/
//	  <hash>/chatSessions/<sessionId>.jsonl
//
// Each .jsonl is append-only and uses a delta encoding:
//
//	{"kind":0,"v":{...full initial state, including selectedModel...}}
//	{"kind":1,"k":["requests",0,"modelState"],"v":{...}}
//	{"kind":2,"k":["requests",0,"response"],"v":"...streamed chunk..."}
//
// kind=0 = snapshot, kind=1 = set value at key path, kind=2 = append.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/blamely/blamely/internal/config"
	"github.com/blamely/blamely/internal/daemon"
	"github.com/blamely/blamely/internal/gitutil"
	"github.com/blamely/blamely/internal/store"
)

const copilotChatPollInterval = 3 * time.Second
const copilotChatStaleCutoff = 24 * time.Hour

// ── Public watcher types ──────────────────────────────────────────────────────

// CopilotChatWatcher implements daemon.Watcher for GitHub Copilot chat sessions
// in VS Code. It watches Code/User/workspaceStorage and always emits tool=copilot.
type CopilotChatWatcher struct {
	// Roots overrides the workspaceStorage scan roots for tests.
	Roots []string
}

func (c *CopilotChatWatcher) Name() string { return "copilot-chat" }

func (c *CopilotChatWatcher) Run(ctx context.Context, sink daemon.Sink) error {
	roots := c.Roots
	if len(roots) == 0 {
		roots = defaultCopilotChatRoots()
	}
	return (&chatSessionWatcher{tool: store.ToolCopilot, roots: roots}).run(ctx, sink)
}

// defaultCopilotChatRoots returns the VS Code workspaceStorage paths for the
// current OS. Only VS Code — Cursor is handled by CursorChatWatcher.
// findChatSessionPath locates a VS Code / Cursor chatSessions JSONL file by
// session UUID (filename is <sessionID>.jsonl under workspaceStorage).
func findChatSessionPath(sessionID string, roots []string) string {
	if sessionID == "" {
		return ""
	}
	name := sessionID + ".jsonl"
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			p := filepath.Join(root, e.Name(), "chatSessions", name)
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return ""
}

// copilotChatSearchRoots returns workspaceStorage roots where Copilot chat
// sessions may live (VS Code and Copilot-in-Cursor).
func copilotChatSearchRoots() []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range append(defaultCopilotChatRoots(), defaultCursorChatRoots()...) {
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	return out
}

func defaultCopilotChatRoots() []string {
	home, err := config.Home()
	if err != nil {
		return nil
	}
	switch runtime.GOOS {
	case "darwin":
		return []string{
			filepath.Join(home, "Library", "Application Support", "Code", "User", "workspaceStorage"),
		}
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			return nil
		}
		return []string{
			filepath.Join(appData, "Code", "User", "workspaceStorage"),
		}
	default:
		return []string{
			filepath.Join(home, ".config", "Code", "User", "workspaceStorage"),
		}
	}
}

// ── Shared implementation ─────────────────────────────────────────────────────

// chatSessionWatcher is the shared JSONL scanner. It is not exported — callers
// use CopilotChatWatcher or CursorChatWatcher. The tool field is fixed at
// construction time: every event emitted by this watcher carries that tool,
// regardless of which model the user selected inside the editor. This keeps
// Copilot and Cursor attribution fully independent.
type chatSessionWatcher struct {
	tool  store.Tool
	roots []string
}

// sessionState is the per-file bookkeeping carried between scans.
type sessionState struct {
	offset    int64     // bytes read so far from this jsonl
	model     string    // last-known display model (provider prefix stripped)
	lastTouch time.Time // for stale eviction

	// textEditGroup detection scans incrementally from tegOffset (its own
	// cursor, independent of offset above) and is deduped by edit key in
	// seenEdits. tegMtime gates re-scans so an unchanged file costs nothing.
	// nextReqIdx is the requests-array length seen so far, used to assign a
	// stable index to whole-new-request appends (k=["requests"]).
	tegMtime   time.Time
	tegOffset  int64
	nextReqIdx int
	seenEdits  map[string]bool
}

func (w *chatSessionWatcher) run(ctx context.Context, sink daemon.Sink) error {
	var mu sync.Mutex
	state := map[string]*sessionState{}

	tick := time.NewTicker(copilotChatPollInterval)
	defer tick.Stop()
	for {
		for _, root := range w.roots {
			w.scanRoot(root, state, &mu, sink)
		}
		w.evictStale(state, &mu)
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

func (w *chatSessionWatcher) scanRoot(root string, state map[string]*sessionState, mu *sync.Mutex, sink daemon.Sink) {
	if root == "" {
		return
	}
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		// Only chatSessions/*.jsonl. The chatEditingSessions sibling directory
		// has different content we don't currently parse.
		if !strings.HasSuffix(p, ".jsonl") || !strings.Contains(p, string(filepath.Separator)+"chatSessions"+string(filepath.Separator)) {
			return nil
		}
		w.handleSessionFile(p, state, mu, sink)
		return nil
	})
}

func (w *chatSessionWatcher) handleSessionFile(path string, state map[string]*sessionState, mu *sync.Mutex, sink daemon.Sink) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return
	}
	mu.Lock()
	st, ok := state[path]
	if !ok {
		st = &sessionState{}
		state[path] = st
	}
	// Reset the streaming offset if the file shrank — VS Code periodically
	// rewrites a session as a fresh snapshot, which would otherwise strand the
	// offset past EOF and silently stop processing.
	if info.Size() < st.offset {
		st.offset = 0
	}
	startOffset := st.offset
	prime := startOffset == 0
	mu.Unlock()

	// textEditGroup detection runs on every scan via its own mtime gate —
	// independent of the append-offset so it survives snapshot rewrites.
	// Must run BEFORE the no-new-bytes return below.
	w.scanTextEdits(path, st, mu, sink, info.ModTime(), info.Size())

	if info.Size() <= startOffset {
		return
	}

	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Seek(startOffset, 0); err != nil {
		return
	}
	r := bufio.NewReaderSize(f, 1<<16)
	now := time.Now()
	for {
		line, err := r.ReadString('\n')
		if line != "" {
			w.handleLine(path, line, st, sink, prime, now, info.ModTime())
		}
		if err != nil {
			break
		}
	}
	newOffset, _ := f.Seek(0, 1)
	mu.Lock()
	st.offset = newOffset
	st.lastTouch = now
	mu.Unlock()
}

// scanTextEdits incrementally scans the bytes appended to the session file
// since the last call (gated by mtime, so an unchanged file is a no-op) and
// records a chat edit for every new textEditGroup it hasn't emitted before.
//
// Scanning incrementally — rather than re-reading the whole file from byte 0
// on every poll — matters for long-running chat sessions: a session's JSONL
// can grow to several MB over many turns, and re-parsing plus re-hashing that
// entire history every 3s would make detection latency grow with session
// size (fast for new sessions, increasingly slow for long ones).
func (w *chatSessionWatcher) scanTextEdits(path string, st *sessionState, mu *sync.Mutex, sink daemon.Sink, mtime time.Time, size int64) {
	mu.Lock()
	if st.seenEdits == nil {
		st.seenEdits = map[string]bool{}
	}
	firstScan := st.tegMtime.IsZero()
	if !mtime.After(st.tegMtime) {
		mu.Unlock()
		return
	}
	st.tegMtime = mtime
	model := st.model
	startOffset := st.tegOffset
	nextReqIdx := st.nextReqIdx
	// VS Code occasionally rewrites a session as a fresh, smaller snapshot —
	// re-scan from the start when that happens (rare); seenEdits is cleared
	// too since the rewritten file may renumber requests.
	if size < startOffset {
		startOffset = 0
		nextReqIdx = 0
		st.seenEdits = map[string]bool{}
		firstScan = true
	}
	mu.Unlock()

	// Suppress emitting on the very first scan of an OLD file (historical edits);
	// always emit once the file is actively being modified.
	emit := !firstScan || time.Since(mtime) <= 2*copilotChatPollInterval

	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	if startOffset > 0 {
		if _, err := f.Seek(startOffset, 0); err != nil {
			return
		}
	}
	br := bufio.NewReaderSize(f, 1<<16)
	for {
		line, rerr := br.ReadString('\n')
		trimmed := strings.TrimRight(line, "\r\n")
		if len(trimmed) > 0 && trimmed[0] == '{' {
			var cl chatLine
			if json.Unmarshal([]byte(trimmed), &cl) == nil {
				// Keep the display model fresh as we scan (used when recording edits).
				if m := findSelectedModel(cl); m != "" {
					model = displayModel(m)
				}
				// textEditGroups are tagged with the chat request index they belong to
				// (requests[N].response[...]) so that edits from different turns never
				// collide in recordTextEditGroup's dedup key, even when both happen to
				// start at the same line (e.g. the model rewrites the whole file again
				// in a follow-up turn).
				switch cl.Kind {
				case 0:
					var snap struct {
						Requests []struct {
							Response []json.RawMessage `json:"response"`
						} `json:"requests"`
					}
					if json.Unmarshal(cl.V, &snap) == nil {
						for reqIdx, r := range snap.Requests {
							for _, part := range r.Response {
								var groups []textEditGroupPart
								findTextEditGroups(part, &groups)
								for i := range groups {
									w.recordTextEditGroup(&groups[i], model, path, st, mu, sink, emit, reqIdx)
								}
							}
						}
						nextReqIdx = len(snap.Requests)
					}
				case 2:
					if reqIdx, ok := requestIndex(cl.K, "response"); ok {
						// Streaming delta: new response part(s) appended to an
						// existing requests[reqIdx].response array.
						var parts []json.RawMessage
						if json.Unmarshal(cl.V, &parts) == nil {
							for _, part := range parts {
								var groups []textEditGroupPart
								findTextEditGroups(part, &groups)
								for i := range groups {
									w.recordTextEditGroup(&groups[i], model, path, st, mu, sink, emit, reqIdx)
								}
							}
						}
					} else if keyPathEquals(cl.K, "requests") {
						// A whole new request (turn) appended to the top-level
						// requests array. Its response array may already be
						// populated — including finalized textEditGroups — in
						// this single delta, e.g. for a fast/non-streamed turn.
						var newReqs []struct {
							Response []json.RawMessage `json:"response"`
						}
						if json.Unmarshal(cl.V, &newReqs) == nil {
							for _, req := range newReqs {
								for _, part := range req.Response {
									var groups []textEditGroupPart
									findTextEditGroups(part, &groups)
									for i := range groups {
										w.recordTextEditGroup(&groups[i], model, path, st, mu, sink, emit, nextReqIdx)
									}
								}
								nextReqIdx++
							}
						}
					}
				}
			}
		}
		if rerr != nil {
			break
		}
	}
	newOffset, _ := f.Seek(0, 1)
	mu.Lock()
	st.tegOffset = newOffset
	st.nextReqIdx = nextReqIdx
	mu.Unlock()
}

func (w *chatSessionWatcher) handleLine(path, line string, st *sessionState, sink daemon.Sink, prime bool, scanTime, fileMTime time.Time) {
	var cl chatLine
	if err := json.Unmarshal([]byte(line), &cl); err != nil {
		return
	}
	switch cl.Kind {
	case 0:
		var snap struct {
			InputState struct {
				SelectedModel json.RawMessage `json:"selectedModel"`
			} `json:"inputState"`
		}
		if json.Unmarshal(cl.V, &snap) == nil {
			st.applyModel(extractSelectedModel(snap.InputState.SelectedModel))
		}
	case 1:
		if keyPathHasSuffix(cl.K, "selectedModel") {
			st.applyModel(extractSelectedModel(cl.V))
		}
	case 2:
		if !keyPathHasSuffix(cl.K, "response") {
			return
		}
		if prime && time.Since(fileMTime) > 2*copilotChatPollInterval {
			return
		}
		ev := daemon.Event{
			When:       scanTime,
			Tool:       string(w.tool),
			Confidence: "low",
			GenType:    "chat",
			Model:      st.model,
			RawMeta: fmt.Sprintf(`{"source":"copilot_chat_session","tool":%q,"chat_session_path":%q}`,
				string(w.tool), path),
		}
		if err := sink.Record(ev); err != nil {
			log.Printf("%s-chat sink: %v", w.tool, err)
		}
	}
}

// recordTextEditGroup records one finalized textEditGroup as a per-file chat
// edit. The tool is always w.tool — the watcher knows which editor it belongs to.
// reqIdx is the chat request (turn) this textEditGroup belongs to, used to keep
// the dedup key below from colliding across separate turns.
func (w *chatSessionWatcher) recordTextEditGroup(teg *textEditGroupPart, model, sessionPath string, st *sessionState, mu *sync.Mutex, sink daemon.Sink, emit bool, reqIdx int) {
	if !teg.Done {
		return
	}
	abs := teg.URI.FsPath
	if abs == "" {
		abs = teg.URI.Path
	}
	if abs == "" || (teg.URI.Scheme != "" && teg.URI.Scheme != "file") {
		return
	}

	var ranges []daemon.LineRange
	var suggested int64
	hasRemoval := false
	for _, grp := range teg.Edits {
		for _, e := range grp {
			// endLineNumber > startLineNumber means this edit's range spans
			// existing lines (VS Code's half-open convention) — a candidate
			// for removed-line attribution, computed below once we have the
			// pre-edit snapshot. Checked regardless of e.Text so pure
			// multi-line deletions (empty replacement text) are caught too.
			if e.Range.EndLineNumber > e.Range.StartLineNumber {
				hasRemoval = true
			}
			if strings.TrimSpace(e.Text) == "" {
				continue
			}
			start := e.Range.StartLineNumber
			if start <= 0 {
				start = 1
			}
			for i, lt := range strings.Split(strings.TrimSuffix(e.Text, "\n"), "\n") {
				ln := start + i
				text := strings.TrimRight(lt, "\r")
				ranges = append(ranges, daemon.LineRange{
					Start: ln, End: ln,
					ContentSHA:     sha256Hex([]byte(text)),
					ContentSHANorm: sha256HexNorm(text),
				})
				suggested++
			}
		}
	}
	if len(ranges) == 0 && !hasRemoval {
		return
	}

	// Dedup key: file + chat request index + startLineNumber of each edit group.
	// Streaming applies write multiple done=true checkpoints for the same region
	// with growing content within ONE turn — keying on start positions collapses
	// those into one emission. The request index keeps separate turns (e.g. a
	// follow-up that rewrites the file again, also starting at line 1) from
	// colliding and silently dropping the later, correct edit.
	key := fmt.Sprintf("%s|%d", abs, reqIdx)
	for _, grp := range teg.Edits {
		for _, e := range grp {
			key += fmt.Sprintf("|%d", e.Range.StartLineNumber)
			break
		}
	}
	mu.Lock()
	if st.seenEdits[key] {
		mu.Unlock()
		return
	}
	st.seenEdits[key] = true
	mu.Unlock()
	if !emit {
		return
	}

	if r, err := filepath.EvalSymlinks(abs); err == nil {
		abs = r
	}
	repo, _ := gitutil.RepoID(abs)
	if repo == "" {
		return
	}
	wt, _ := gitutil.Toplevel(abs)
	rel := abs
	if wt != "" {
		if r, err := filepath.Rel(wt, abs); err == nil && !strings.HasPrefix(r, "..") {
			rel = r
		}
	}

	// Copilot's textEditGroup edits carry only the new text and a line range,
	// not the replaced text — fetch the daemon's cached snapshot (the file's
	// content as of its last recorded edit) so multi-line replacements can
	// still be checked for removed lines.
	var removed []DeletedLineHash
	if hasRemoval {
		if snapshot, ok := fetchSnapshot(repo, rel); ok {
			for _, grp := range teg.Edits {
				for _, e := range grp {
					if e.Range.EndLineNumber > e.Range.StartLineNumber {
						removed = append(removed, removedLinesForTextEditRange(snapshot, e.Range.StartLineNumber, e.Range.EndLineNumber, e.Text)...)
					}
				}
			}
		}
	}

	ev := daemon.Event{
		When:           time.Now(),
		Tool:           string(w.tool),
		Confidence:     "high",
		GenType:        "chat",
		Model:          model,
		RepoPath:       repo,
		FilePath:       rel,
		Lines:          ranges,
		SuggestedLines: suggested,
		RemovedLines:   toDaemonRemovedLines(removed),
		RawMeta: fmt.Sprintf(`{"source":"copilot_chat_textedit","tool":%q,"chat_session_path":%q}`,
			string(w.tool), sessionPath),
	}
	if err := sink.Record(ev); err != nil {
		log.Printf("%s-chat textedit sink: %v", w.tool, err)
	} else {
		log.Printf("%s-chat: apply %s lines=%d model=%s", w.tool, rel, len(ranges), model)
	}
}

// applyModel updates the session's display model from a raw selectedModel
// identifier (e.g. "copilot/gpt-5-mini"). The tool is NOT changed here —
// it is fixed on the watcher, not per-session.
func (st *sessionState) applyModel(identifier string) {
	if identifier == "" {
		return
	}
	st.model = displayModel(identifier)
}

// displayModel strips the provider prefix from a chat model identifier so the
// report's per-model rollup reads "gpt-5-mini" rather than "copilot/gpt-5-mini".
func displayModel(identifier string) string {
	if i := strings.IndexByte(identifier, '/'); i >= 0 {
		return identifier[i+1:]
	}
	return identifier
}

func (w *chatSessionWatcher) evictStale(state map[string]*sessionState, mu *sync.Mutex) {
	mu.Lock()
	defer mu.Unlock()
	cutoff := time.Now().Add(-copilotChatStaleCutoff)
	for k, st := range state {
		if st.lastTouch.Before(cutoff) {
			delete(state, k)
		}
	}
}

// ── Shared JSONL parsing helpers ──────────────────────────────────────────────

// chatLine is the minimal delta-encoding envelope. K and V are RawMessage so
// we don't pay for schema we don't use.
type chatLine struct {
	Kind int             `json:"kind"`
	K    json.RawMessage `json:"k"`
	V    json.RawMessage `json:"v"`
}

// textEditGroupPart is the response-part shape the chat panel writes when it
// applies edits: target URI, text edits with line ranges, and a done flag.
type textEditGroupPart struct {
	Kind string `json:"kind"`
	URI  struct {
		FsPath string `json:"fsPath"`
		Path   string `json:"path"`
		Scheme string `json:"scheme"`
	} `json:"uri"`
	Edits [][]struct {
		Text  string `json:"text"`
		Range struct {
			StartLineNumber int `json:"startLineNumber"`
			EndLineNumber   int `json:"endLineNumber"`
		} `json:"range"`
	} `json:"edits"`
	Done bool `json:"done"`
}

// findSelectedModel extracts a selectedModel identifier from a delta/snapshot
// line, tolerating both the inputState.selectedModel delta and snapshot shapes.
func findSelectedModel(cl chatLine) string {
	if keyPathHasSuffix(cl.K, "selectedModel") {
		return extractSelectedModel(cl.V)
	}
	if cl.Kind == 0 {
		var snap struct {
			InputState struct {
				SelectedModel json.RawMessage `json:"selectedModel"`
			} `json:"inputState"`
		}
		if json.Unmarshal(cl.V, &snap) == nil {
			return extractSelectedModel(snap.InputState.SelectedModel)
		}
	}
	return ""
}

// findTextEditGroups recursively collects every finalized textEditGroup object
// anywhere within raw (response arrays, snapshot request trees, etc.).
func findTextEditGroups(raw json.RawMessage, out *[]textEditGroupPart) {
	if len(raw) == 0 {
		return
	}
	switch raw[0] {
	case '{':
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) != nil {
			return
		}
		if k, ok := obj["kind"]; ok {
			var ks string
			if json.Unmarshal(k, &ks) == nil && ks == "textEditGroup" {
				var teg textEditGroupPart
				if json.Unmarshal(raw, &teg) == nil {
					*out = append(*out, teg)
				}
				return
			}
		}
		for _, v := range obj {
			findTextEditGroups(v, out)
		}
	case '[':
		var arr []json.RawMessage
		if json.Unmarshal(raw, &arr) != nil {
			return
		}
		for _, v := range arr {
			findTextEditGroups(v, out)
		}
	}
}

// extractSelectedModel pulls selectedModel.identifier from a kind=0 or kind=1
// value blob. Tolerant of flat and wrapped shapes.
func extractSelectedModel(v json.RawMessage) string {
	if len(v) == 0 {
		return ""
	}
	var probe struct {
		SelectedModel struct {
			Identifier string `json:"identifier"`
			Family     string `json:"family"`
		} `json:"selectedModel"`
		Identifier string `json:"identifier"`
		Family     string `json:"family"`
	}
	if err := json.Unmarshal(v, &probe); err != nil {
		return ""
	}
	if probe.SelectedModel.Identifier != "" {
		return probe.SelectedModel.Identifier
	}
	if probe.SelectedModel.Family != "" {
		return probe.SelectedModel.Family
	}
	if probe.Identifier != "" {
		return probe.Identifier
	}
	return probe.Family
}

// keyPathHasSuffix reports whether the JSON-encoded k ends with the given
// string element (e.g. ["requests", 0, "response"] ends with "response").
func keyPathHasSuffix(k json.RawMessage, suffix string) bool {
	if len(k) == 0 {
		return false
	}
	var path []json.RawMessage
	if err := json.Unmarshal(k, &path); err != nil || len(path) == 0 {
		return false
	}
	last := path[len(path)-1]
	var s string
	if err := json.Unmarshal(last, &s); err != nil {
		return false
	}
	return s == suffix
}

// keyPathEquals reports whether the JSON-encoded k is exactly the
// single-element path [elem] (e.g. ["requests"]).
func keyPathEquals(k json.RawMessage, elem string) bool {
	if len(k) == 0 {
		return false
	}
	var path []json.RawMessage
	if err := json.Unmarshal(k, &path); err != nil || len(path) != 1 {
		return false
	}
	var s string
	if err := json.Unmarshal(path[0], &s); err != nil {
		return false
	}
	return s == elem
}
