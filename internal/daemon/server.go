package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/blamely/blamely/internal/config"
	"github.com/blamely/blamely/internal/store"
)

// UnixHTTPClient returns an *http.Client that routes all requests through the
// Unix domain socket at sockPath, regardless of the URL host. Use
// "http://unix/<path>" as the request URL (the host is ignored).
func UnixHTTPClient(sockPath string) *http.Client {
	return &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
			},
		},
	}
}

type Server struct {
	db   *store.DB
	http *http.Server
}

// EditPayload is the JSON body POSTed to /edit by tool integrations.
type EditPayload struct {
	Tool       string `json:"tool"`
	Confidence string `json:"confidence,omitempty"`
	// GenType: chat | cli | completion | unknown
	GenType          string `json:"gen_type,omitempty"`
	RepoPath         string `json:"repo_path"`
	FilePath         string `json:"file_path"`
	Model            string `json:"model,omitempty"`
	InputTokens      *int64 `json:"input_tokens,omitempty"`
	OutputTokens     *int64 `json:"output_tokens,omitempty"`
	CacheReadTokens  *int64 `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens *int64 `json:"cache_write_tokens,omitempty"`
	HashBefore       string `json:"hash_before,omitempty"`
	HashAfter        string `json:"hash_after,omitempty"`
	// SuggestedLines: AI's original suggestion size before user edits.
	SuggestedLines int64              `json:"suggested_lines,omitempty"`
	Lines          []Range            `json:"lines"`
	RemovedLines   []RemovedLineHash  `json:"removed_lines,omitempty"`
	RawMeta        string             `json:"raw_meta,omitempty"`
	// Branch is the editor's checked-out branch when the edit was made. Optional:
	// the daemon resolves it from repo_path if empty (e.g. watcher-sourced edits).
	Branch string `json:"branch,omitempty"`
}

type Range struct {
	Start          int    `json:"start"`
	End            int    `json:"end"`
	ContentSHA     string `json:"content_sha,omitempty"`
	ContentSHANorm string `json:"content_sha_norm,omitempty"`
}

// Watchers is the list of background tailers (Codex/Cursor/Copilot) the
// daemon will run. It is assigned by cmd/blamely before calling Run so the
// daemon package itself doesn't have to import the tools package.
var Watchers []Watcher

// DBWatcherFactory, if set, is called at daemon startup to create a Watcher
// that needs DB access (e.g. VelocityWatcher). Assigned by cmd/blamely.
//
// Use DBWatcherFactories for additional DB-backed watchers; both are appended
// to the runtime watcher list. DBWatcherFactory is retained for backward
// compatibility with the original single-factory wiring.
var DBWatcherFactory func(db *store.DB) Watcher

// DBWatcherFactories is the list of additional DB-backed watcher factories.
// Each is invoked once at daemon startup; the resulting Watcher is appended
// to the run list alongside DBWatcherFactory's product.
var DBWatcherFactories []func(db *store.DB) Watcher

func Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	db, err := store.Open()
	if err != nil {
		return err
	}
	defer db.Close()

	s := &Server{db: db}

	watchers := append([]Watcher{}, Watchers...) // copy so DB-backed watchers can be appended
	if DBWatcherFactory != nil {
		watchers = append(watchers, DBWatcherFactory(db))
	}
	for _, f := range DBWatcherFactories {
		watchers = append(watchers, f(db))
	}
	if len(watchers) > 0 {
		go runWatchers(ctx, db, watchers)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.health)
	mux.HandleFunc("/edit", s.ingest)
	mux.HandleFunc("/fs", s.fsEvent)
	mux.HandleFunc("/snapshot", s.snapshot)

	// Prefer a Unix domain socket — it bypasses network security tools (e.g.
	// Trend Micro) that intercept localhost TCP. Falls back to a random TCP port
	// on systems that don't support AF_UNIX (older Windows builds).
	sockPath, sockErr := config.SocketFile()
	var listener net.Listener
	if sockErr == nil {
		_ = os.Remove(sockPath) // clean up stale socket from a previous run
		if l, err := net.Listen("unix", sockPath); err == nil {
			listener = l
			if err := writeSocketFile(sockPath); err != nil {
				_ = listener.Close()
				_ = os.Remove(sockPath)
				return err
			}
			defer removeSocketFile()
			go watchListenerFile(ctx, sockPath, cancel)
			fmt.Fprintf(os.Stderr, "blamelyd listening on %s\n", sockPath)
		}
	}
	if listener == nil {
		// AF_UNIX unavailable or path error — fall back to TCP.
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return fmt.Errorf("listen: %w", err)
		}
		listener = l
		addr := listener.Addr().(*net.TCPAddr)
		if err := writePortFile(addr.Port); err != nil {
			_ = listener.Close()
			return err
		}
		defer removePortFile()
		if portPath, err := config.PortFile(); err == nil {
			go watchListenerFile(ctx, portPath, cancel)
		}
		fmt.Fprintf(os.Stderr, "blamelyd listening on 127.0.0.1:%d\n", addr.Port)
	}

	s.http = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := s.http.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, sCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer sCancel()
		return s.http.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `{"ok":true}`)
}

// snapshot returns the cached "before" content for repo/file — the file's
// content as of the last recorded edit, used by whole-file-overwrite tools
// (Write/write_file) and Copilot's textEditGroup edits to compute removed-line
// hashes when they have no "before" content of their own. If nothing has been
// cached yet (this file's first recorded edit), falls back to the file's
// content at HEAD; found=false if neither is available (e.g. a brand-new
// untracked file — correctly yields no removed lines).
func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	repo := r.URL.Query().Get("repo")
	file := r.URL.Query().Get("file")
	if repo == "" || file == "" {
		http.Error(w, "repo, file required", http.StatusBadRequest)
		return
	}
	content, ok, err := s.db.GetFileSnapshot(repo, file)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		content, ok = headFileContent(repo, file)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Content string `json:"content"`
		Found   bool   `json:"found"`
	}{Content: content, Found: ok})
}

// headFileContent returns file's content at HEAD in repoPath, or ok=false if
// the repo has no HEAD yet or the file isn't tracked there (e.g. a new file).
func headFileContent(repoPath, file string) (string, bool) {
	out, err := exec.Command("git", "-C", repoPath, "show", "HEAD:"+filepath.ToSlash(file)).Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

func (s *Server) ingest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var p EditPayload
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&p); err != nil {
		http.Error(w, fmt.Sprintf("decode: %v", err), http.StatusBadRequest)
		return
	}
	if err := validateAndStore(s.db, p); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func validateAndStore(db *store.DB, p EditPayload) error {
	if p.RepoPath == "" || p.FilePath == "" {
		return fmt.Errorf("repo_path, file_path required")
	}
	tool := store.Tool(strings.ToLower(p.Tool))
	gt := store.GenType(strings.ToLower(p.GenType))
	if gt == "" {
		gt = store.GenTypeUnknown
	}
	// Tool is required EXCEPT for human edits, which travel as
	// tool="" + gen_type=human. Anything else with an empty tool is a
	// caller bug.
	if tool == "" && gt != store.GenTypeHuman {
		return fmt.Errorf("tool required (or use gen_type=human for empty tool)")
	}
	switch tool {
	case "",
		store.ToolClaude, store.ToolCursor, store.ToolCodex, store.ToolCopilot, store.ToolGemini, store.ToolCopyPaste,
		store.ToolHuman: // accepted only so the daemon doesn't reject legacy clients mid-upgrade
	default:
		return fmt.Errorf("unknown tool %q", p.Tool)
	}
	conf := store.Confidence(p.Confidence)
	if conf == "" {
		conf = defaultConfidence(tool)
	}
	e := store.Edit{
		TimestampNanos: time.Now().UnixNano(),
		RepoPath:       p.RepoPath,
		FilePath:       p.FilePath,
		Tool:           tool,
		Confidence:     conf,
		GenType:        gt,
		SuggestedLines: p.SuggestedLines,
	}
	if p.Model != "" {
		e.Model.Valid = true
		e.Model.String = p.Model
	}

	enrichChatEdit(db, &e)

	setNullInt(&e.InputTokens, p.InputTokens)
	setNullInt(&e.OutputTokens, p.OutputTokens)
	setNullInt(&e.CacheReadTokens, p.CacheReadTokens)
	setNullInt(&e.CacheWriteTokens, p.CacheWriteTokens)
	if p.HashBefore != "" {
		e.HashBefore.Valid = true
		e.HashBefore.String = p.HashBefore
	}
	if p.HashAfter != "" {
		e.HashAfter.Valid = true
		e.HashAfter.String = p.HashAfter
	}
	if p.RawMeta != "" {
		e.RawMeta.Valid = true
		e.RawMeta.String = p.RawMeta
	}
	for _, r := range p.Lines {
		if r.Start <= 0 || r.End < r.Start {
			return fmt.Errorf("invalid line range [%d,%d]", r.Start, r.End)
		}
		e.Lines = append(e.Lines, store.EditLine{
			StartLine: r.Start, EndLine: r.End, ContentSHA: r.ContentSHA, ContentSHANorm: r.ContentSHANorm,
		})
	}
	for _, rl := range p.RemovedLines {
		if rl.ContentSHA == "" {
			continue
		}
		e.RemovedLines = append(e.RemovedLines, store.RemovedLineHash{
			ContentSHA: rl.ContentSHA, ContentSHANorm: rl.ContentSHANorm,
		})
	}
	sessions.resolve(db, &e, p.Branch)
	if _, err := db.InsertEdit(e); err != nil {
		return err
	}
	updateFileSnapshot(db, p.RepoPath, p.FilePath)
	return nil
}

// updateFileSnapshot caches repo/file's current on-disk content as the
// "before" baseline for the next edit to this file (see (*Server).snapshot).
// Best-effort: a read failure (file deleted, permission error, race) just
// means the next edit falls back to HEAD instead of this edit's result.
func updateFileSnapshot(db *store.DB, repoPath, filePath string) {
	data, err := os.ReadFile(filepath.Join(repoPath, filePath))
	if err != nil {
		return
	}
	_ = db.SetFileSnapshot(repoPath, filePath, string(data), time.Now().UnixNano())
}

// chatEnrichWindowNanos is the look-back/forward window for correlating a
// committed edit with the chat-session markers emitted by the chat watcher.
const chatEnrichWindowNanos = int64(60 * 1e9)     // 60 seconds — gen_type correlation
const chatModelWindowNanos = int64(30 * 60 * 1e9) // 30 minutes — sticky model backfill

// enrichChatEdit upgrades a chat-tool edit's gen_type + model from recent
// chat-session markers. The plugin / log / velocity paths can't tell a
// chat-panel apply from an inline Tab accept, so a chat-generated edit arrives
// as gen_type=completion with no model. The chat-session watcher writes
// gen_type=chat markers (carrying the selected model) whenever a response
// streams into the chat JSONL; we look those up here so the committed edit
// reflects the chat panel correctly. Runs for both copilot and cursor; a marker
// only exists when the user actually used that tool's chat panel, so a pure
// Tab-completion session is left as gen_type=completion.
func enrichChatEdit(db *store.DB, e *store.Edit) {
	switch e.Tool {
	case store.ToolCopilot, store.ToolCursor:
	default:
		return
	}
	now := e.TimestampNanos
	// Never override a CONFIRMED inline-completion accept. confidence=high on a
	// completion means the editor plugin saw the inline-suggest commit command
	// (or IDE accept action) fire — that's a real Tab/inline accept, not a chat
	// apply, even if the user happened to have a chat panel open nearby.
	confirmedCompletion := e.Confidence == store.ConfidenceHigh && e.GenType == store.GenTypeCompletion
	if e.GenType != store.GenTypeChat && !confirmedCompletion {
		if recent := db.LatestChatGenTypeNear(e.Tool, now, chatEnrichWindowNanos); recent == string(store.GenTypeChat) {
			e.GenType = store.GenTypeChat
		}
	}
	if !e.Model.Valid || e.Model.String == "" {
		// Best-effort model backfill for ANY copilot/cursor edit (chat AND
		// inline completion). The selected model is sticky across a session, so
		// use a generous window — an inline completion gets the model the user
		// most recently had active, even if their last chat was minutes ago.
		if m := db.LatestChatModelNear(e.Tool, e.RepoPath, now, chatModelWindowNanos); m != "" {
			e.Model = sql.NullString{Valid: true, String: m}
		}
	}
}

func defaultConfidence(t store.Tool) store.Confidence {
	switch t {
	case store.ToolClaude, store.ToolCodex:
		return store.ConfidenceHigh
	case store.ToolCursor:
		return store.ConfidenceHigh
	case store.ToolCopilot:
		return store.ConfidenceLow
	default:
		return store.ConfidenceHigh
	}
}

func setNullInt(dst *sql.NullInt64, src *int64) {
	if src != nil {
		dst.Valid = true
		dst.Int64 = *src
	}
}

// writeSocketFile is a no-op: net.Listen("unix", sockPath) already creates the
// socket file. Its existence is the indicator — there is nothing to write into it.
func writeSocketFile(_ string) error { return nil }

func removeSocketFile() {
	if p, err := config.SocketFile(); err == nil {
		_ = os.Remove(p)
	}
}

func writePortFile(port int) error {
	p, err := config.PortFile()
	if err != nil {
		return err
	}
	return os.WriteFile(p, []byte(fmt.Sprintf("%d", port)), 0o644)
}

func removePortFile() {
	if p, err := config.PortFile(); err == nil {
		_ = os.Remove(p)
	}
}

// watchListenerFile periodically confirms that path — the daemon's socket or
// port file, its sole discovery mechanism for `blamely status` and the
// record/snapshot hooks — still exists on disk. If something removes it out
// from under a live listener (e.g. a racing daemon instance's startup
// cleanup at server.go:131, or external deletion), the bound listener becomes
// permanently unreachable to new clients with no way to recreate the file in
// place. Cancelling ctx triggers a clean shutdown so the platform service
// manager (launchd KeepAlive / systemd Restart=always) immediately restarts
// us with a fresh file.
func watchListenerFile(ctx context.Context, path string, cancel context.CancelFunc) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := os.Stat(path); err != nil {
				cancel()
				return
			}
		}
	}
}

// ReadSocket returns the Unix domain socket path if the socket file exists,
// or an error if the daemon is not running. The socket file's existence is the
// indicator — its content must not be read (it is a socket, not a plain file).
func ReadSocket() (string, error) {
	p, err := config.SocketFile()
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("socket file %s: %w", p, err)
	}
	return p, nil
}

// ReadPort is retained for backward compatibility with callers that still use
// the TCP port. Returns an error if the port file does not exist.
func ReadPort() (int, error) {
	p, err := config.PortFile()
	if err != nil {
		return 0, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return 0, fmt.Errorf("read port file %s: %w", p, err)
	}
	var port int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &port); err != nil {
		return 0, fmt.Errorf("parse port: %w", err)
	}
	return port, nil
}
