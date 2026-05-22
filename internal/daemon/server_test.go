package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blamely/blamely/internal/store"
)

// openTestDB returns an in-temp-dir DB the caller doesn't need to clean up.
func openTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.OpenAt(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// fakeHome redirects config.Home() to a temp dir so PortFile() doesn't
// touch the developer's real ~/.blamely while the test is running.
func fakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")
	return home
}

// minimalPayload is the smallest body that should pass validation. Each test
// that wants to mutate one field copies this and overrides what it cares
// about.
func minimalPayload() EditPayload {
	return EditPayload{
		Tool:     "claude",
		RepoPath: "/repo",
		FilePath: "main.go",
		Lines:    []Range{{Start: 1, End: 3}},
	}
}

// ---------- validateAndStore ----------

func TestValidateAndStore_AcceptsMinimalPayload(t *testing.T) {
	db := openTestDB(t)
	if err := validateAndStore(db, minimalPayload()); err != nil {
		t.Fatalf("validateAndStore: %v", err)
	}
	edits, err := db.EditsForFileSince("/repo", "main.go", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	e := edits[0]
	if e.Tool != store.ToolClaude {
		t.Errorf("tool = %q, want claude", e.Tool)
	}
	// Defaults: confidence=high (claude), gen_type=unknown
	if e.Confidence != store.ConfidenceHigh {
		t.Errorf("confidence default = %q, want high", e.Confidence)
	}
	if e.GenType != store.GenTypeUnknown {
		t.Errorf("gen_type default = %q, want unknown", e.GenType)
	}
	if len(e.Lines) != 1 || e.Lines[0].StartLine != 1 || e.Lines[0].EndLine != 3 {
		t.Errorf("lines roundtrip wrong: %+v", e.Lines)
	}
}

func TestValidateAndStore_RejectsMissingRequiredFields(t *testing.T) {
	db := openTestDB(t)
	cases := map[string]EditPayload{
		"tool missing":      {RepoPath: "/r", FilePath: "f"},
		"repo_path missing": {Tool: "claude", FilePath: "f"},
		"file_path missing": {Tool: "claude", RepoPath: "/r"},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			err := validateAndStore(db, p)
			if err == nil || !strings.Contains(err.Error(), "required") {
				t.Errorf("expected 'required' error, got: %v", err)
			}
		})
	}
}

func TestValidateAndStore_RejectsUnknownTool(t *testing.T) {
	db := openTestDB(t)
	p := minimalPayload()
	p.Tool = "windsurf"
	err := validateAndStore(db, p)
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("expected 'unknown tool' error, got: %v", err)
	}
}

func TestValidateAndStore_RejectsInvalidLineRanges(t *testing.T) {
	db := openTestDB(t)
	cases := []Range{
		{Start: 0, End: 5},  // start must be > 0
		{Start: -1, End: 5}, // negative start
		{Start: 5, End: 3},  // end < start
	}
	for _, r := range cases {
		p := minimalPayload()
		p.Lines = []Range{r}
		err := validateAndStore(db, p)
		if err == nil || !strings.Contains(err.Error(), "invalid line range") {
			t.Errorf("range %+v: expected invalid-line-range error, got: %v", r, err)
		}
	}
}

func TestValidateAndStore_NormalizesToolCaseAndDefaultsConfidence(t *testing.T) {
	db := openTestDB(t)
	// Uppercase tool name; copilot has a different default confidence (Low)
	// than the others, which is the discriminating signal here.
	p := minimalPayload()
	p.Tool = "COPILOT"
	if err := validateAndStore(db, p); err != nil {
		t.Fatal(err)
	}
	edits, err := db.EditsForFileSince("/repo", "main.go", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	if edits[0].Tool != store.ToolCopilot {
		t.Errorf("tool case not normalized: got %q", edits[0].Tool)
	}
	if edits[0].Confidence != store.ConfidenceLow {
		t.Errorf("copilot default confidence = %q, want low", edits[0].Confidence)
	}
}

func TestValidateAndStore_RoundtripsOptionalFields(t *testing.T) {
	db := openTestDB(t)
	in := int64(1000)
	out := int64(50)
	p := minimalPayload()
	p.Model = "claude-opus-4-7"
	p.InputTokens = &in
	p.OutputTokens = &out
	p.HashBefore = "aaa"
	p.HashAfter = "bbb"
	p.SuggestedLines = 7
	p.GenType = "chat"
	p.Confidence = "medium"
	if err := validateAndStore(db, p); err != nil {
		t.Fatal(err)
	}
	edits, _ := db.EditsForFileSince("/repo", "main.go", 0)
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	e := edits[0]
	if !e.Model.Valid || e.Model.String != "claude-opus-4-7" {
		t.Errorf("model not stored: %+v", e.Model)
	}
	if !e.InputTokens.Valid || e.InputTokens.Int64 != 1000 {
		t.Errorf("input_tokens not stored: %+v", e.InputTokens)
	}
	if e.HashBefore.String != "aaa" || e.HashAfter.String != "bbb" {
		t.Errorf("hashes not stored: before=%v after=%v", e.HashBefore, e.HashAfter)
	}
	if e.SuggestedLines != 7 {
		t.Errorf("suggested_lines = %d, want 7", e.SuggestedLines)
	}
	if e.GenType != store.GenTypeChat {
		t.Errorf("gen_type = %q, want chat", e.GenType)
	}
	if e.Confidence != store.ConfidenceMedium {
		t.Errorf("confidence override = %q, want medium", e.Confidence)
	}
}

func TestDefaultConfidence(t *testing.T) {
	cases := map[store.Tool]store.Confidence{
		store.ToolClaude:  store.ConfidenceHigh,
		store.ToolCodex:   store.ConfidenceHigh,
		store.ToolCursor:  store.ConfidenceHigh,
		store.ToolCopilot: store.ConfidenceLow,
		store.ToolHuman:   store.ConfidenceHigh,
	}
	for tool, want := range cases {
		if got := defaultConfidence(tool); got != want {
			t.Errorf("defaultConfidence(%q) = %q, want %q", tool, got, want)
		}
	}
}

// ---------- HTTP handlers ----------

func TestServer_Health(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	s.health(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	body, _ := io.ReadAll(w.Body)
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("parse body: %v (raw=%q)", err, body)
	}
	if m["ok"] != true {
		t.Errorf("body = %v, want {ok:true}", m)
	}
}

func TestServer_Ingest_HappyPath(t *testing.T) {
	db := openTestDB(t)
	s := &Server{db: db}
	body, _ := json.Marshal(minimalPayload())
	req := httptest.NewRequest(http.MethodPost, "/edit", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.ingest(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
	edits, _ := db.EditsForFileSince("/repo", "main.go", 0)
	if len(edits) != 1 {
		t.Errorf("expected edit persisted, got %d", len(edits))
	}
}

func TestServer_Ingest_RejectsNonPost(t *testing.T) {
	s := &Server{db: openTestDB(t)}
	req := httptest.NewRequest(http.MethodGet, "/edit", nil)
	w := httptest.NewRecorder()
	s.ingest(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestServer_Ingest_RejectsBadJSON(t *testing.T) {
	s := &Server{db: openTestDB(t)}
	req := httptest.NewRequest(http.MethodPost, "/edit", strings.NewReader("{not json"))
	w := httptest.NewRecorder()
	s.ingest(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestServer_Ingest_RejectsValidationFailure(t *testing.T) {
	s := &Server{db: openTestDB(t)}
	// Empty body parses fine but fails validateAndStore (missing tool).
	req := httptest.NewRequest(http.MethodPost, "/edit", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	s.ingest(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// ---------- port file ----------

func TestPortFile_WriteThenRead(t *testing.T) {
	home := fakeHome(t)
	// EnsureBlamelyDir is what production code (Run) calls; mimic it.
	if err := mkdirP(filepath.Join(home, ".blamely")); err != nil {
		t.Fatal(err)
	}
	if err := writePortFile(54321); err != nil {
		t.Fatal(err)
	}
	got, err := ReadPort()
	if err != nil {
		t.Fatal(err)
	}
	if got != 54321 {
		t.Errorf("port = %d, want 54321", got)
	}
}

func TestPortFile_Remove(t *testing.T) {
	home := fakeHome(t)
	if err := mkdirP(filepath.Join(home, ".blamely")); err != nil {
		t.Fatal(err)
	}
	if err := writePortFile(1234); err != nil {
		t.Fatal(err)
	}
	removePortFile()
	if _, err := ReadPort(); err == nil {
		t.Error("expected ReadPort to fail after removePortFile")
	}
}

func TestReadPort_GarbageContent(t *testing.T) {
	home := fakeHome(t)
	if err := mkdirP(filepath.Join(home, ".blamely")); err != nil {
		t.Fatal(err)
	}
	// Write something that isn't a number.
	if err := writeFile(filepath.Join(home, ".blamely", "daemon.port"), "abc\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPort(); err == nil || !strings.Contains(err.Error(), "parse port") {
		t.Errorf("expected parse error, got: %v", err)
	}
}

// ---------- WaitForReady ----------

func TestWaitForReady_HitsHealthy(t *testing.T) {
	fakeHome(t)
	// Spin up a real listener answering 200 on /health.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	port := portFromURL(t, srv.URL)

	// Write the port file so ReadPort() picks it up.
	if err := mkdirP(homeBlamely(t)); err != nil {
		t.Fatal(err)
	}
	if err := writePortFile(port); err != nil {
		t.Fatal(err)
	}

	got, err := WaitForReady(2 * time.Second)
	if err != nil {
		t.Fatalf("WaitForReady: %v", err)
	}
	if got != port {
		t.Errorf("port = %d, want %d", got, port)
	}
}

func TestWaitForReady_TimesOutWhenPortFileMissing(t *testing.T) {
	fakeHome(t)
	// No port file written; should time out quickly.
	_, err := WaitForReady(300 * time.Millisecond)
	if err == nil {
		t.Error("expected timeout error")
	}
}

// ---------- dbSink ----------

func TestDBSink_Record_Defaults(t *testing.T) {
	db := openTestDB(t)
	s := &dbSink{db: db}
	ev := Event{
		Tool:     "cursor",
		RepoPath: "/repo",
		FilePath: "x.go",
		Lines:    []LineRange{{Start: 1, End: 2}},
		// When is zero — Record should fall back to time.Now().
	}
	before := time.Now().UnixNano()
	if err := s.Record(ev); err != nil {
		t.Fatal(err)
	}
	after := time.Now().UnixNano()

	edits, _ := db.EditsForFileSince("/repo", "x.go", 0)
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	e := edits[0]
	if e.TimestampNanos < before || e.TimestampNanos > after {
		t.Errorf("ts %d not in [%d,%d]", e.TimestampNanos, before, after)
	}
	if e.Confidence != store.ConfidenceHigh {
		t.Errorf("cursor default confidence = %q, want high", e.Confidence)
	}
	if e.GenType != store.GenTypeUnknown {
		t.Errorf("gen_type default = %q, want unknown", e.GenType)
	}
}

func TestDBSink_Record_PreservesExplicitTimestamp(t *testing.T) {
	db := openTestDB(t)
	s := &dbSink{db: db}
	when := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	ev := Event{
		When:     when,
		Tool:     "codex",
		RepoPath: "/r",
		FilePath: "f.go",
		Lines:    []LineRange{{Start: 1, End: 1}},
	}
	if err := s.Record(ev); err != nil {
		t.Fatal(err)
	}
	edits, _ := db.EditsForFileSince("/r", "f.go", 0)
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	if edits[0].TimestampNanos != when.UnixNano() {
		t.Errorf("ts = %d, want %d", edits[0].TimestampNanos, when.UnixNano())
	}
}

func TestDBSink_Record_RejectsUnknownTool(t *testing.T) {
	s := &dbSink{db: openTestDB(t)}
	err := s.Record(Event{Tool: "windsurf", RepoPath: "/r", FilePath: "f"})
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("expected unknown-tool error, got: %v", err)
	}
}

func TestDBSink_Record_SkipsInvalidRanges(t *testing.T) {
	// Watchers can produce noisy line data; Record should silently drop
	// degenerate ranges rather than failing the whole insert.
	db := openTestDB(t)
	s := &dbSink{db: db}
	ev := Event{
		Tool:     "claude",
		RepoPath: "/r",
		FilePath: "f.go",
		Lines: []LineRange{
			{Start: 5, End: 3}, // invalid: dropped
			{Start: 1, End: 2}, // valid: kept
			{Start: 0, End: 1}, // invalid: dropped
		},
	}
	if err := s.Record(ev); err != nil {
		t.Fatal(err)
	}
	edits, _ := db.EditsForFileSince("/r", "f.go", 0)
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	if len(edits[0].Lines) != 1 {
		t.Errorf("expected 1 valid line range kept, got %d: %+v", len(edits[0].Lines), edits[0].Lines)
	}
}

// ---------- runWatchers ----------

// fakeWatcher records that it ran and then blocks until ctx is canceled.
type fakeWatcher struct {
	name    string
	started chan struct{}
	err     error
}

func (w *fakeWatcher) Name() string { return w.name }
func (w *fakeWatcher) Run(ctx context.Context, _ Sink) error {
	close(w.started)
	if w.err != nil {
		return w.err
	}
	<-ctx.Done()
	return nil
}

func TestRunWatchers_StartsAllAndExitsOnContextCancel(t *testing.T) {
	db := openTestDB(t)
	w1 := &fakeWatcher{name: "w1", started: make(chan struct{})}
	w2 := &fakeWatcher{name: "w2", started: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		runWatchers(ctx, db, []Watcher{w1, w2})
		close(done)
	}()

	// Both watchers must start.
	for _, w := range []*fakeWatcher{w1, w2} {
		select {
		case <-w.started:
		case <-time.After(time.Second):
			t.Fatalf("%s did not start", w.name)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runWatchers did not return after context cancel")
	}
}

func TestRunWatchers_OneErroringWatcherDoesNotAffectOthers(t *testing.T) {
	// A watcher returning an error should be logged and not bring down siblings.
	db := openTestDB(t)
	bad := &fakeWatcher{name: "bad", started: make(chan struct{}), err: errors.New("boom")}
	good := &fakeWatcher{name: "good", started: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runWatchers(ctx, db, []Watcher{bad, good})
	}()

	select {
	case <-good.started:
	case <-time.After(time.Second):
		t.Fatal("good watcher did not start")
	}
	cancel()
	wg.Wait()
}

func TestRunWatchers_NoWatchers_NoOp(t *testing.T) {
	// With no watchers the function should return immediately even if ctx isn't
	// cancelled — otherwise daemons configured without watchers would hang.
	db := openTestDB(t)
	done := make(chan struct{})
	go func() {
		runWatchers(context.Background(), db, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runWatchers with empty list should return immediately")
	}
}

// ---------- small helpers ----------

func mkdirP(p string) error {
	return os.MkdirAll(p, 0o755)
}

func writeFile(p, content string) error {
	return os.WriteFile(p, []byte(content), 0o644)
}

func homeBlamely(t *testing.T) string {
	t.Helper()
	// HOME is already set by fakeHome; mirror the path config.PortFile uses.
	return filepath.Join(os.Getenv("HOME"), ".blamely")
}

func portFromURL(t *testing.T, url string) int {
	t.Helper()
	// url is like "http://127.0.0.1:NNNNN" — split off the port.
	i := strings.LastIndex(url, ":")
	if i < 0 {
		t.Fatalf("bad URL: %s", url)
	}
	var port int
	if _, err := fmt.Sscanf(url[i+1:], "%d", &port); err != nil {
		t.Fatalf("parse port from %s: %v", url, err)
	}
	return port
}
