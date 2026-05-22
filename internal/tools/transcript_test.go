package tools

import (
	"strings"
	"testing"
)

func TestScanTranscriptUsage_Empty(t *testing.T) {
	u, err := scanTranscriptUsage(strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if u != nil {
		t.Fatalf("expected nil for empty transcript, got %+v", u)
	}
}

func TestScanTranscriptUsage_NoAssistantMessages(t *testing.T) {
	input := `{"type":"user","message":{"role":"user","content":"hello"}}` + "\n" +
		`{"type":"tool_result","content":"result"}` + "\n"
	u, err := scanTranscriptUsage(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if u != nil {
		t.Fatalf("expected nil when no assistant messages, got %+v", u)
	}
}

func TestScanTranscriptUsage_AssistantWithoutUsage(t *testing.T) {
	input := `{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-7"}}` + "\n"
	u, err := scanTranscriptUsage(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if u != nil {
		t.Fatalf("expected nil when usage is zero/absent, got %+v", u)
	}
}

func TestScanTranscriptUsage_FullUsage(t *testing.T) {
	input := `{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":1200,"output_tokens":340,"cache_read_input_tokens":800,"cache_creation_input_tokens":150}}}` + "\n"
	u, err := scanTranscriptUsage(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if u == nil {
		t.Fatal("expected non-nil usage")
	}
	if u.Model != "claude-opus-4-7" {
		t.Errorf("model: want claude-opus-4-7, got %s", u.Model)
	}
	if u.InputTokens != 1200 {
		t.Errorf("input_tokens: want 1200, got %d", u.InputTokens)
	}
	if u.OutputTokens != 340 {
		t.Errorf("output_tokens: want 340, got %d", u.OutputTokens)
	}
	if u.CacheReadTokens != 800 {
		t.Errorf("cache_read: want 800, got %d", u.CacheReadTokens)
	}
	if u.CacheWriteTokens != 150 {
		t.Errorf("cache_write: want 150, got %d", u.CacheWriteTokens)
	}
}

func TestScanTranscriptUsage_LastAssistantTurnWins(t *testing.T) {
	// Two assistant turns — the test should return the LAST one with usage.
	input := `{"type":"assistant","message":{"role":"assistant","model":"claude-sonnet-4-6","usage":{"input_tokens":100,"output_tokens":10}}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":500,"output_tokens":50}}}` + "\n"
	u, err := scanTranscriptUsage(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if u == nil {
		t.Fatal("expected non-nil")
	}
	if u.Model != "claude-opus-4-7" {
		t.Errorf("want last model claude-opus-4-7, got %s", u.Model)
	}
	if u.InputTokens != 500 {
		t.Errorf("want 500 input_tokens from last turn, got %d", u.InputTokens)
	}
}

func TestScanTranscriptUsage_MalformedLinesSkipped(t *testing.T) {
	input := "not json at all\n" +
		`{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":42,"output_tokens":7}}}` + "\n"
	u, err := scanTranscriptUsage(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if u == nil || u.InputTokens != 42 {
		t.Fatalf("expected usage from valid line, got %+v", u)
	}
}
