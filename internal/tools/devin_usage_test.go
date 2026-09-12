package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const devinTranscriptFixture = `{
  "schema_version": "ATIF-v1.7",
  "session_id": "axiomatic-jeans",
  "agent": {"name": "devin", "version": "3000.10.21", "model_name": "SWE-2 High"},
  "steps": [
    {"step_id": 1, "source": "system", "message": "You are Devin"},
    {"step_id": 2, "source": "user", "message": "add a checkbox"},
    {"step_id": 3, "source": "agent", "message": "reading", "model_name": "swe-2-high",
     "metrics": {"prompt_tokens": 11819, "completion_tokens": 141, "cached_tokens": 4620},
     "extra": {"generation_model": "swe-2-high"}},
    {"step_id": 4, "source": "agent", "message": "done", "model_name": "swe-2-high",
     "metrics": {"prompt_tokens": 17909, "completion_tokens": 110, "cached_tokens": 16579},
     "extra": {"generation_model": "swe-2-high"}}
  ],
  "final_metrics": {"total_prompt_tokens": 100465, "total_completion_tokens": 2551, "total_cached_tokens": 87170, "total_steps": 4}
}`

func TestScanDevinTranscriptUsage(t *testing.T) {
	u, err := scanDevinTranscriptUsage(strings.NewReader(devinTranscriptFixture))
	if err != nil {
		t.Fatal(err)
	}
	if u == nil {
		t.Fatal("expected usage, got nil")
	}
	if u.Model != "swe-2-high" {
		t.Errorf("model = %q, want swe-2-high (per-step uid, not the SWE-2 High display label)", u.Model)
	}
	if u.InputTokens != 17909 || u.OutputTokens != 110 || u.CacheReadTokens != 16579 {
		t.Errorf("tokens = in %d / out %d / cached %d, want latest step 17909 / 110 / 16579",
			u.InputTokens, u.OutputTokens, u.CacheReadTokens)
	}
	if u.CacheWriteTokens != 0 {
		t.Errorf("cacheWriteTokens = %d, want 0 (Devin reports none)", u.CacheWriteTokens)
	}
}

func TestScanDevinTranscriptUsage_FallsBackToAgentModelName(t *testing.T) {
	u, err := scanDevinTranscriptUsage(strings.NewReader(`{
	  "session_id": "x", "agent": {"name": "devin", "model_name": "SWE-2 High"},
	  "steps": [{"step_id": 1, "source": "user", "message": "hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if u == nil || u.Model != "SWE-2 High" {
		t.Fatalf("usage = %+v, want model SWE-2 High from agent header", u)
	}
	if u.InputTokens != 0 || u.OutputTokens != 0 {
		t.Errorf("expected no tokens before the first agent step, got %+v", u)
	}
}

func TestScanDevinTranscriptUsage_PartialWriteStillYieldsModel(t *testing.T) {
	// Devin rewrites the transcript after every step; a hook can observe a
	// truncated file. The model must survive even when the JSON does not parse.
	full := devinTranscriptFixture
	cut := full[:strings.LastIndex(full, `"metrics"`)]
	u, err := scanDevinTranscriptUsage(strings.NewReader(cut))
	if err != nil {
		t.Fatal(err)
	}
	if u == nil || u.Model != "swe-2-high" {
		t.Fatalf("usage = %+v, want model swe-2-high recovered from partial transcript", u)
	}
}

func TestScanDevinTranscriptUsage_NothingUsefulReturnsNil(t *testing.T) {
	for _, in := range []string{``, `not json at all`, `{"session_id":"x","steps":[]}`} {
		u, err := scanDevinTranscriptUsage(strings.NewReader(in))
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if u != nil {
			t.Errorf("%q: expected nil usage, got %+v", in, u)
		}
	}
}

func TestReadDevinTranscriptUsageFrom(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "axiomatic-jeans.json"), []byte(devinTranscriptFixture), 0o644); err != nil {
		t.Fatal(err)
	}

	u, err := readDevinTranscriptUsageFrom(base, "axiomatic-jeans")
	if err != nil {
		t.Fatal(err)
	}
	if u == nil || u.Model != "swe-2-high" || u.OutputTokens != 110 {
		t.Fatalf("usage = %+v, want swe-2-high / 110 output tokens", u)
	}

	// Unknown session → nil, no error (best-effort enrichment).
	u, err = readDevinTranscriptUsageFrom(base, "no-such-session")
	if err != nil || u != nil {
		t.Fatalf("missing transcript: usage=%+v err=%v, want nil/nil", u, err)
	}

	// A session id that is not a bare file name must not be joined into a path.
	u, err = readDevinTranscriptUsageFrom(base, filepath.Join("..", "axiomatic-jeans"))
	if err != nil || u != nil {
		t.Fatalf("traversal id: usage=%+v err=%v, want nil/nil", u, err)
	}
}

func TestReadHookUsage_DevinUsesTranscriptBySessionID(t *testing.T) {
	// readHookUsage's devin case must consult the Devin transcript reader — it
	// cannot fall through to the Claude parser (no transcript_path) and must
	// not return nil unconditionally anymore.
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "stirring-marquess.json"), []byte(devinTranscriptFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := devinTranscriptsDir
	devinTranscriptsDir = func() (string, error) { return base, nil }
	t.Cleanup(func() { devinTranscriptsDir = prev })

	u := readHookUsage(hookUsageOptions{tool: "devin", sessionID: "stirring-marquess"})
	if u == nil || u.Model != "swe-2-high" {
		t.Fatalf("readHookUsage(devin) = %+v, want model swe-2-high", u)
	}
}
