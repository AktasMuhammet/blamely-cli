package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blamely/blamely/internal/store"
)

const copilotCliEventsFixture = `{"type":"session.start","data":{}}
{"type":"assistant.message","data":{"messageId":"m1","content":"","toolRequests":[{"toolCallId":"t1","name":"create"}],"outputTokens":0}}
{"type":"tool.execution_complete","data":{"toolCallId":"t1","model":"claude-haiku-4.5","success":true}}
{"type":"assistant.message","data":{"messageId":"m2","content":"Done!","toolRequests":[],"outputTokens":22}}
{"type":"assistant.turn_end","data":{"turnId":"1"}}`

func TestScanCopilotCliUsage(t *testing.T) {
	u, err := scanCopilotCliUsage(strings.NewReader(copilotCliEventsFixture))
	if err != nil {
		t.Fatal(err)
	}
	if u == nil {
		t.Fatal("expected usage, got nil")
	}
	if u.Model != "claude-haiku-4.5" {
		t.Errorf("model = %q, want claude-haiku-4.5", u.Model)
	}
	if u.OutputTokens != 22 {
		t.Errorf("outputTokens = %d, want 22", u.OutputTokens)
	}
	// Input/cache tokens aren't present in the CLI log.
	if u.InputTokens != 0 {
		t.Errorf("inputTokens = %d, want 0 (not in log)", u.InputTokens)
	}
}

func TestScanCopilotCliUsage_NoUsageReturnsNil(t *testing.T) {
	u, err := scanCopilotCliUsage(strings.NewReader(`{"type":"session.start","data":{}}` + "\n" + `{"type":"user.message","data":{"content":"hi"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if u != nil {
		t.Fatalf("expected nil usage, got %+v", u)
	}
}

func TestReadCopilotCliUsageFrom(t *testing.T) {
	base := t.TempDir()
	sid := "0aa99421-68aa-41ef-8960-1d4f413b490d"
	dir := filepath.Join(base, sid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(copilotCliEventsFixture), 0o644); err != nil {
		t.Fatal(err)
	}

	u, err := readCopilotCliUsageFrom(base, sid)
	if err != nil {
		t.Fatal(err)
	}
	if u == nil || u.Model != "claude-haiku-4.5" || u.OutputTokens != 22 {
		t.Fatalf("got %+v, want model=claude-haiku-4.5 outputTokens=22", u)
	}

	// Missing session → nil, no error (not every hook fires inside a CLI session).
	if u, err := readCopilotCliUsageFrom(base, "does-not-exist"); err != nil || u != nil {
		t.Fatalf("missing session: got (%+v, %v), want (nil, nil)", u, err)
	}
}

const copilotShutdownFixture = `{"type":"assistant.message","data":{"outputTokens":929,"model":"gpt-5-mini"}}
{"type":"session.shutdown","data":{"modelMetrics":{"gpt-5-mini":{"usage":{"inputTokens":26512,"outputTokens":929,"cacheReadTokens":15872,"cacheWriteTokens":0,"reasoningTokens":704}},"gpt-5.2":{"usage":{"inputTokens":26214,"outputTokens":540,"cacheReadTokens":13184,"cacheWriteTokens":0,"reasoningTokens":329}}}}}`

func TestScanCopilotCliSessionUsage(t *testing.T) {
	m, err := scanCopilotCliSessionUsage(strings.NewReader(copilotShutdownFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 {
		t.Fatalf("got %d models, want 2", len(m))
	}
	mini := m["gpt-5-mini"]
	if mini.InputTokens != 26512 || mini.OutputTokens != 929 || mini.CacheReadTokens != 15872 || mini.ReasoningTokens != 704 {
		t.Fatalf("gpt-5-mini usage wrong: %+v", mini)
	}
	if m["gpt-5.2"].InputTokens != 26214 {
		t.Fatalf("gpt-5.2 input wrong: %+v", m["gpt-5.2"])
	}
}

func TestScanCopilotCliSessionUsage_NoShutdownYet(t *testing.T) {
	m, err := scanCopilotCliSessionUsage(strings.NewReader(copilotCliEventsFixture)) // no session.shutdown
	if err != nil {
		t.Fatal(err)
	}
	if m != nil {
		t.Fatalf("expected nil (session still running), got %+v", m)
	}
}

func TestCopilotCliUsageWatcher_RecordsAndResumes(t *testing.T) {
	base := t.TempDir()
	db, err := store.OpenAt(filepath.Join(base, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	stateDir := filepath.Join(base, "session-state")
	sid := "11111111-2222-3333-4444-555555555555"
	dir := filepath.Join(stateDir, sid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(p, []byte(copilotShutdownFixture), 0o644); err != nil {
		t.Fatal(err)
	}

	w := &CopilotCliUsageWatcher{SessionStateDir: stateDir, DB: db}
	w.scan(stateDir)

	got, ok := db.LoadSessionUsage(sid, "copilot", "gpt-5-mini")
	if !ok || got.InputTokens != 26512 || got.OutputTokens != 929 || got.CacheReadTokens != 15872 {
		t.Fatalf("session usage not recorded: ok=%v %+v", ok, got)
	}
	if u, _ := db.LoadSessionUsage(sid, "copilot", "gpt-5.2"); u.InputTokens != 26214 {
		t.Fatalf("second model not recorded: %+v", u)
	}

	// Unchanged file → watermark skip (still present, no error).
	w.scan(stateDir)
	if u, ok := db.LoadSessionUsage(sid, "copilot", "gpt-5-mini"); !ok || u.InputTokens != 26512 {
		t.Fatalf("after no-op rescan: %+v", u)
	}
}
