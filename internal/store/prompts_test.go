package store

import (
	"testing"
)

func TestUserPrompts_UpsertAndRead(t *testing.T) {
	db := openTestDB(t)

	if err := db.UpsertUserPrompts("s1", "/repo", "claude", []string{"add flag", "also append"}, 100); err != nil {
		t.Fatalf("UpsertUserPrompts: %v", err)
	}
	got, err := db.UserPromptsForSession("s1")
	if err != nil {
		t.Fatalf("UserPromptsForSession: %v", err)
	}
	if len(got) != 2 || got[0] != "add flag" || got[1] != "also append" {
		t.Fatalf("got %v, want [add flag, also append]", got)
	}

	// Re-upsert with an edited first turn + a new third turn → no duplicates, order kept.
	if err := db.UpsertUserPrompts("s1", "/repo", "claude", []string{"add flag v2", "also append", "and clean"}, 200); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	got, _ = db.UserPromptsForSession("s1")
	if len(got) != 3 || got[0] != "add flag v2" || got[2] != "and clean" {
		t.Fatalf("after re-upsert got %v", got)
	}
}

func TestSessionTranscriptsForPeriod_ParsesRawMeta(t *testing.T) {
	db := openTestDB(t)
	e := Edit{
		TimestampNanos: 150,
		RepoPath:       "/repo",
		FilePath:       "a.go",
		Tool:           ToolClaude,
		Confidence:     "high",
		GenType:        GenTypeChat,
	}
	e.RawMeta.Valid = true
	e.RawMeta.String = `{"session_id":"sess-9","tool":"Edit","transcript_path":"/t/sess-9.jsonl"}`
	e.Lines = []EditLine{{StartLine: 1, EndLine: 1}}
	if _, err := db.InsertEdit(e); err != nil {
		t.Fatalf("InsertEdit: %v", err)
	}
	out, err := db.SessionTranscriptsForPeriod("/repo", 0, 1000)
	if err != nil {
		t.Fatalf("SessionTranscriptsForPeriod: %v", err)
	}
	if len(out) != 1 || out[0].SessionID != "sess-9" || out[0].TranscriptPath != "/t/sess-9.jsonl" {
		t.Fatalf("got %+v", out)
	}
}
