package tools

// CopilotChatWatcher reads the VS Code / Cursor chat-session JSONL files that
// the chat panel persists per workspace, and emits a session marker each time
// the model produces a new response — tagged with the selected model AND the
// owning tool. The same chatSessions/ directory holds both GitHub Copilot Chat
// and Cursor's own chat; we classify each session by its selectedModel
// identifier ("copilot/…" ⇒ copilot, otherwise ⇒ cursor) so the two are
// attributed separately rather than all being credited to Copilot.
//
// Why this matters
// ----------------
// The base CopilotWatcher only knows "the globalStorage SQLite mutated",
// which is a fuzzy signal with no model attached. The chat-session files
// give us:
//
//   - The exact selectedModel.identifier (e.g. "copilot/gpt-5-mini",
//     "copilot/gpt-4o", "copilot/claude-3-5-sonnet").
//   - A real "Copilot just produced response content" event (the kind=2
//     line whose key path ends in `response`), so we mark activity at
//     the moment text actually streams in, not when storage flushes.
//
// The model + gen_type flow downstream via store.LatestChatModelNear /
// LatestChatGenTypeNear: when the chat-panel apply lands as a plugin/log edit,
// the daemon's enrichChatEdit re-stamps the row with the chat gen_type and the
// most-recent model from this watcher's emissions. The chat_session_path in
// raw_meta lets the attribution step re-open the JSONL for conversation + tokens.
//
// File layout (per inspection — undocumented, subject to change):
//
//	~/Library/Application Support/{Code,Cursor}/User/workspaceStorage/
//	  <hash>/chatSessions/<sessionId>.jsonl
//
// Each .jsonl is append-only and uses a delta encoding:
//
//	{"kind":0,"v":{...full initial state, including selectedModel...}}
//	{"kind":1,"k":["requests",0,"modelState"],"v":{...}}
//	{"kind":2,"k":["requests",0,"response"],"v":"...streamed chunk..."}
//
// kind=0 = snapshot, kind=1 = set value at key path, kind=2 = append.
// We only care about the model in kind=0/kind=1, and the response-arrival
// signal in kind=2.

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
	"github.com/blamely/blamely/internal/store"
)

// copilotChatPollInterval controls how often we re-scan the workspaceStorage
// roots for chat-session activity. Chat responses don't need millisecond
// latency to be useful downstream, and a tighter loop just burns syscalls.
const copilotChatPollInterval = 3 * time.Second

// copilotChatStaleCutoff drops in-memory state for session files that
// haven't been touched in this long. Keeps the watcher bounded for users
// with hundreds of historical sessions.
const copilotChatStaleCutoff = 24 * time.Hour

// CopilotChatWatcher implements daemon.Watcher.
type CopilotChatWatcher struct {
	// Roots overrides the workspaceStorage scan roots for tests. If empty,
	// the platform defaults are used.
	Roots []string
}

func (c *CopilotChatWatcher) Name() string { return "copilot-chat" }

// sessionState is the per-file bookkeeping carried between scans.
type sessionState struct {
	offset    int64      // bytes read so far from this jsonl
	model     string     // last-known display model for this session (provider prefix stripped)
	tool      store.Tool // tool the session belongs to, classified from selectedModel
	lastTouch time.Time  // for stale eviction
}

func (c *CopilotChatWatcher) Run(ctx context.Context, sink daemon.Sink) error {
	roots := c.Roots
	if len(roots) == 0 {
		roots = defaultCopilotChatRoots()
	}

	var mu sync.Mutex
	state := map[string]*sessionState{}

	tick := time.NewTicker(copilotChatPollInterval)
	defer tick.Stop()
	for {
		for _, root := range roots {
			c.scanRoot(root, state, &mu, sink)
		}
		c.evictStale(state, &mu)
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// scanRoot finds every chatSessions/*.jsonl under root and walks any new
// bytes appended since the last visit.
func (c *CopilotChatWatcher) scanRoot(root string, state map[string]*sessionState, mu *sync.Mutex, sink daemon.Sink) {
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
		// Only chatSessions/*.jsonl. The chatEditingSessions sibling has
		// different content we don't currently parse.
		if !strings.HasSuffix(p, ".jsonl") || !strings.Contains(p, string(filepath.Separator)+"chatSessions"+string(filepath.Separator)) {
			return nil
		}
		c.handleSessionFile(p, state, mu, sink)
		return nil
	})
}

func (c *CopilotChatWatcher) handleSessionFile(path string, state map[string]*sessionState, mu *sync.Mutex, sink daemon.Sink) {
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
	// First time we see this file we still want the initial snapshot's
	// model — but we DON'T want to emit a flood of "response arrived"
	// markers for messages that streamed before the daemon started. The
	// solution: read the file once, capture the model from kind=0, but
	// only emit markers for response lines whose underlying file mtime
	// is recent (within the poll interval × a small factor).
	startOffset := st.offset
	prime := startOffset == 0
	mu.Unlock()

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
			c.handleLine(path, line, st, sink, prime, now, info.ModTime())
		}
		if err != nil {
			break
		}
	}
	newOffset, _ := f.Seek(0, 1) // current position
	mu.Lock()
	st.offset = newOffset
	st.lastTouch = now
	mu.Unlock()
}

// chatLine is the minimal envelope we decode. `K` and `V` are intentionally
// json.RawMessage so we don't pay for schema we don't use.
type chatLine struct {
	Kind int             `json:"kind"`
	K    json.RawMessage `json:"k"`
	V    json.RawMessage `json:"v"`
}

func (c *CopilotChatWatcher) handleLine(path, line string, st *sessionState, sink daemon.Sink, prime bool, scanTime, fileMTime time.Time) {
	var cl chatLine
	if err := json.Unmarshal([]byte(line), &cl); err != nil {
		return
	}
	switch cl.Kind {
	case 0:
		// Initial snapshot — the selected model lives at
		// inputState.selectedModel (NOT the top level), so reach into it
		// before handing the blob to extractSelectedModel.
		var snap struct {
			InputState struct {
				SelectedModel json.RawMessage `json:"selectedModel"`
			} `json:"inputState"`
		}
		if json.Unmarshal(cl.V, &snap) == nil {
			st.applyModel(extractSelectedModel(snap.InputState.SelectedModel))
		}
	case 1:
		// Scalar update at a key path. The selected model arrives at
		// ["inputState","selectedModel"]. (Note: "modelState" is a per-request
		// status enum {value,completedAt}, NOT the model — don't read it here.)
		if keyPathHasSuffix(cl.K, "selectedModel") {
			st.applyModel(extractSelectedModel(cl.V))
		}
	case 2:
		// Append at a key path — `response` chunks are how the chat panel
		// streams its reply. The first response chunk for a request is the
		// signal "the model just generated code/text in the chat panel".
		if !keyPathHasSuffix(cl.K, "response") {
			return
		}
		// During the prime pass for a file we already had, skip emitting
		// for old chunks — only emit if the underlying file is being
		// updated right now (within ~2 poll intervals).
		if prime && time.Since(fileMTime) > 2*copilotChatPollInterval {
			return
		}
		tool := st.tool
		if tool == "" {
			// Model not seen yet (deltas can arrive before the snapshot's
			// selectedModel). Default to copilot — the historical behaviour —
			// rather than dropping the signal entirely.
			tool = store.ToolCopilot
		}
		ev := daemon.Event{
			When:       scanTime,
			Tool:       string(tool),
			Confidence: "low",
			GenType:    "chat",
			Model:      st.model,
			// chat_session_path lets the attribution step re-open this JSONL
			// to extract the conversation + token usage for the commit's note.
			RawMeta: fmt.Sprintf(`{"source":"copilot_chat_session","tool":%q,"chat_session_path":%q}`,
				string(tool), path),
		}
		if err := sink.Record(ev); err != nil {
			log.Printf("copilot-chat sink: %v", err)
		}
	}
}

// applyModel records the session's display model and re-classifies the owning
// tool from a raw selectedModel.identifier (e.g. "copilot/gpt-5-mini"). Empty
// identifiers are ignored so a transient delta doesn't wipe a known model.
func (st *sessionState) applyModel(identifier string) {
	if identifier == "" {
		return
	}
	st.tool = classifyChatTool(identifier)
	st.model = displayModel(identifier)
}

// classifyChatTool decides which tool a chat session belongs to from its
// selectedModel identifier. GitHub Copilot Chat prefixes every model with
// "copilot/" (e.g. "copilot/gpt-5-mini", "copilot/claude-opus-4.6"); anything
// else is Cursor's own chat. The same chatSessions/ directory under Cursor's
// workspaceStorage holds both, so this prefix is how we keep them separate.
func classifyChatTool(identifier string) store.Tool {
	if strings.HasPrefix(strings.ToLower(identifier), "copilot/") {
		return store.ToolCopilot
	}
	return store.ToolCursor
}

// displayModel strips the provider prefix from a chat model identifier so the
// report's per-model rollup keys read "gpt-5-mini" rather than
// "copilot/gpt-5-mini". Identifiers without a "/" are returned unchanged.
func displayModel(identifier string) string {
	if i := strings.IndexByte(identifier, '/'); i >= 0 {
		return identifier[i+1:]
	}
	return identifier
}

// extractSelectedModel pulls selectedModel.identifier from the bytes of a
// kind=0 or kind=1 value blob. Tolerant of either flat ("identifier":...)
// or wrapped ("selectedModel":{"identifier":...}) shapes; both appear in
// the wild depending on whether the line is the full snapshot or a
// modelState delta.
func extractSelectedModel(v json.RawMessage) string {
	if len(v) == 0 {
		return ""
	}
	var probe struct {
		SelectedModel struct {
			Identifier string `json:"identifier"`
			Family     string `json:"family"`
		} `json:"selectedModel"`
		// Some payloads put the model fields at the top level (modelState
		// deltas, occasional snapshot variants).
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

// keyPathHasSuffix reports whether the JSON-encoded `k` ends with the given
// string element. The `k` field is a heterogeneous array of strings and
// indices (["requests", 0, "response"]); we only need to match the trailing
// string element.
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

func (c *CopilotChatWatcher) evictStale(state map[string]*sessionState, mu *sync.Mutex) {
	mu.Lock()
	defer mu.Unlock()
	cutoff := time.Now().Add(-copilotChatStaleCutoff)
	for k, st := range state {
		if st.lastTouch.Before(cutoff) {
			delete(state, k)
		}
	}
}

// defaultCopilotChatRoots returns the workspaceStorage roots blamely scans
// for chat-session JSONL files across VS Code and Cursor on the current OS.
func defaultCopilotChatRoots() []string {
	home, err := config.Home()
	if err != nil {
		return nil
	}
	switch runtime.GOOS {
	case "darwin":
		return []string{
			filepath.Join(home, "Library", "Application Support", "Code", "User", "workspaceStorage"),
			filepath.Join(home, "Library", "Application Support", "Cursor", "User", "workspaceStorage"),
		}
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			return nil
		}
		return []string{
			filepath.Join(appData, "Code", "User", "workspaceStorage"),
			filepath.Join(appData, "Cursor", "User", "workspaceStorage"),
		}
	default:
		return []string{
			filepath.Join(home, ".config", "Code", "User", "workspaceStorage"),
			filepath.Join(home, ".config", "Cursor", "User", "workspaceStorage"),
		}
	}
}
