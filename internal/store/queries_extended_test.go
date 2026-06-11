package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// ── helpers ──────────────────────────────────────────────────────────────────

func insertEdit(t *testing.T, db *DB, repo, file string, tool Tool, gt GenType, ts int64, rawMeta string) int64 {
	t.Helper()
	e := Edit{
		TimestampNanos: ts,
		RepoPath:       repo,
		FilePath:       file,
		Tool:           tool,
		Confidence:     ConfidenceHigh,
		GenType:        gt,
	}
	if rawMeta != "" {
		e.RawMeta = sql.NullString{Valid: true, String: rawMeta}
	}
	id, err := db.InsertEdit(e)
	if err != nil {
		t.Fatalf("insertEdit: %v", err)
	}
	return id
}

func mustNote(t *testing.T, db *DB, sha, repo string, ts int64) {
	t.Helper()
	if err := db.MarkCommitNoted(sha, repo, ts); err != nil {
		t.Fatalf("MarkCommitNoted: %v", err)
	}
}

// ── KnownCommits ─────────────────────────────────────────────────────────────

func TestKnownCommits_EmptyRepos(t *testing.T) {
	db := openTestDB(t)
	rows, err := db.KnownCommits(nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("want 0, got %d", len(rows))
	}
}

func TestKnownCommits_ReturnsNewestFirst(t *testing.T) {
	db := openTestDB(t)
	const repo = "/r"
	base := int64(1_000_000_000_000)
	mustNote(t, db, "sha1", repo, base+100)
	mustNote(t, db, "sha2", repo, base+200)
	mustNote(t, db, "sha3", repo, base+50)

	rows, err := db.KnownCommits([]string{repo}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("want 3, got %d", len(rows))
	}
	if rows[0].SHA != "sha2" {
		t.Errorf("newest-first: want sha2, got %s", rows[0].SHA)
	}
}

func TestKnownCommits_FiltersBySince(t *testing.T) {
	db := openTestDB(t)
	const repo = "/r"
	base := int64(1_000_000_000_000)
	mustNote(t, db, "old", repo, base)
	mustNote(t, db, "new", repo, base+500)

	rows, err := db.KnownCommits([]string{repo}, base+1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].SHA != "new" {
		t.Errorf("since filter: want [new], got %v", rows)
	}
}

func TestKnownCommits_FiltersByRepo(t *testing.T) {
	db := openTestDB(t)
	base := int64(1_000_000_000_000)
	mustNote(t, db, "sha-a", "/a", base)
	mustNote(t, db, "sha-b", "/b", base)

	rows, err := db.KnownCommits([]string{"/a"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].SHA != "sha-a" {
		t.Errorf("repo filter: want [sha-a], got %v", rows)
	}
}

func TestKnownCommits_MultipleRepos(t *testing.T) {
	db := openTestDB(t)
	base := int64(1_000_000_000_000)
	mustNote(t, db, "sha-a", "/a", base)
	mustNote(t, db, "sha-b", "/b", base+1)

	rows, err := db.KnownCommits([]string{"/a", "/b"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Errorf("multi-repo: want 2, got %d", len(rows))
	}
}

// ── KnownRepoPaths ───────────────────────────────────────────────────────────

func TestKnownRepoPaths_Empty(t *testing.T) {
	db := openTestDB(t)
	paths, err := db.KnownRepoPaths()
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Errorf("want 0, got %d", len(paths))
	}
}

func TestKnownRepoPaths_Distinct(t *testing.T) {
	db := openTestDB(t)
	ts := time.Now().UnixNano()
	for _, repo := range []string{"/a", "/b", "/a", "/c"} {
		insertEdit(t, db, repo, "f.go", ToolClaude, GenTypeChat, ts, "")
		ts++
	}
	paths, err := db.KnownRepoPaths()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, p := range paths {
		if seen[p] {
			t.Errorf("duplicate repo path: %s", p)
		}
		seen[p] = true
	}
	if len(paths) != 3 {
		t.Errorf("want 3 distinct paths, got %d: %v", len(paths), paths)
	}
}

func TestKnownRepoPaths_ExcludesEmpty(t *testing.T) {
	db := openTestDB(t)
	// Insert an edit with empty repo_path (chat-session marker pattern).
	if _, err := db.InsertEdit(Edit{
		TimestampNanos: time.Now().UnixNano(),
		RepoPath:       "",
		FilePath:       "",
		Tool:           ToolCopilot,
		Confidence:     ConfidenceLow,
	}); err != nil {
		t.Fatal(err)
	}
	paths, err := db.KnownRepoPaths()
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Errorf("empty repo_path should be excluded, got %v", paths)
	}
}

// ── UpgradeRecentCompletionsToChat ───────────────────────────────────────────

func TestUpgradeRecentCompletionsToChat_Basic(t *testing.T) {
	db := openTestDB(t)
	ts := time.Now().UnixNano()
	// Low-confidence completion — should be upgraded.
	if _, err := db.InsertEdit(Edit{
		TimestampNanos: ts,
		RepoPath:       "/r", FilePath: "f.go",
		Tool: ToolCopilot, Confidence: ConfidenceLow,
		GenType: GenTypeCompletion,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpgradeRecentCompletionsToChat(ToolCopilot, ts, int64(60*1e9)); err != nil {
		t.Fatal(err)
	}
	var gt string
	db.QueryRow(`SELECT gen_type FROM edits WHERE file_path='f.go'`).Scan(&gt) //nolint
	if gt != "chat" {
		t.Errorf("low-confidence completion should be upgraded to chat, got %q", gt)
	}
}

func TestUpgradeRecentCompletionsToChat_HighConfidenceSkipped(t *testing.T) {
	db := openTestDB(t)
	ts := time.Now().UnixNano()
	// High-confidence completion (confirmed Tab accept) must NOT be upgraded.
	if _, err := db.InsertEdit(Edit{
		TimestampNanos: ts,
		RepoPath:       "/r", FilePath: "g.go",
		Tool: ToolCopilot, Confidence: ConfidenceHigh,
		GenType: GenTypeCompletion,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpgradeRecentCompletionsToChat(ToolCopilot, ts, int64(60*1e9)); err != nil {
		t.Fatal(err)
	}
	var gt string
	db.QueryRow(`SELECT gen_type FROM edits WHERE file_path='g.go'`).Scan(&gt) //nolint
	if gt != "completion" {
		t.Errorf("high-confidence completion must not be upgraded, got %q", gt)
	}
}

func TestUpgradeRecentCompletionsToChat_OutsideWindow(t *testing.T) {
	db := openTestDB(t)
	base := int64(1_000_000_000_000)
	// Edit 2 minutes before window — outside a 60s window.
	if _, err := db.InsertEdit(Edit{
		TimestampNanos: base - int64(120*1e9),
		RepoPath:       "/r", FilePath: "h.go",
		Tool: ToolCopilot, Confidence: ConfidenceLow,
		GenType: GenTypeCompletion,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpgradeRecentCompletionsToChat(ToolCopilot, base, int64(60*1e9)); err != nil {
		t.Fatal(err)
	}
	var gt string
	db.QueryRow(`SELECT gen_type FROM edits WHERE file_path='h.go'`).Scan(&gt) //nolint
	if gt != "completion" {
		t.Errorf("outside-window completion must not be upgraded, got %q", gt)
	}
}

func TestUpgradeRecentCompletionsToChat_WrongTool(t *testing.T) {
	db := openTestDB(t)
	ts := time.Now().UnixNano()
	if _, err := db.InsertEdit(Edit{
		TimestampNanos: ts,
		RepoPath:       "/r", FilePath: "i.go",
		Tool: ToolCursor, Confidence: ConfidenceLow,
		GenType: GenTypeCompletion,
	}); err != nil {
		t.Fatal(err)
	}
	// Upgrading copilot should not touch cursor rows.
	if err := db.UpgradeRecentCompletionsToChat(ToolCopilot, ts, int64(60*1e9)); err != nil {
		t.Fatal(err)
	}
	var gt string
	db.QueryRow(`SELECT gen_type FROM edits WHERE file_path='i.go'`).Scan(&gt) //nolint
	if gt != "completion" {
		t.Errorf("wrong-tool row must not be upgraded, got %q", gt)
	}
}

// ── UpsertUserPrompts / UserPromptsForSession ─────────────────────────────────

func TestUpsertUserPrompts_RoundTrip(t *testing.T) {
	db := openTestDB(t)
	prompts := []string{"hello", "world", "foo"}
	if err := db.UpsertUserPrompts("sess1", "/r", "claude", prompts, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	got, err := db.UserPromptsForSession("sess1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != "hello" || got[1] != "world" || got[2] != "foo" {
		t.Errorf("prompts roundtrip: want %v, got %v", prompts, got)
	}
}

func TestUpsertUserPrompts_Idempotent(t *testing.T) {
	db := openTestDB(t)
	ts := time.Now().UnixNano()
	_ = db.UpsertUserPrompts("sess2", "/r", "claude", []string{"a", "b"}, ts)
	// Re-upsert with updated text.
	_ = db.UpsertUserPrompts("sess2", "/r", "claude", []string{"a-new", "b-new"}, ts)
	got, _ := db.UserPromptsForSession("sess2")
	if len(got) != 2 || got[0] != "a-new" {
		t.Errorf("upsert must overwrite existing prompts: got %v", got)
	}
}

func TestUpsertUserPrompts_EmptySessionOrPrompts(t *testing.T) {
	db := openTestDB(t)
	// Both should be no-ops, not errors.
	if err := db.UpsertUserPrompts("", "/r", "claude", []string{"x"}, 0); err != nil {
		t.Errorf("empty session: %v", err)
	}
	if err := db.UpsertUserPrompts("sess3", "/r", "claude", nil, 0); err != nil {
		t.Errorf("nil prompts: %v", err)
	}
}

func TestUserPromptsForSession_Missing(t *testing.T) {
	db := openTestDB(t)
	got, err := db.UserPromptsForSession("no-such-session")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("missing session should return empty slice, got %v", got)
	}
}

// ── TranscriptPathsForPeriod ──────────────────────────────────────────────────

func TestTranscriptPathsForPeriod_ExtractsDistinct(t *testing.T) {
	db := openTestDB(t)
	base := int64(1_000_000_000_000)
	for i, path := range []string{"/a.jsonl", "/a.jsonl", "/b.jsonl"} {
		raw, _ := json.Marshal(map[string]string{"transcript_path": path})
		insertEdit(t, db, "/r", fmt.Sprintf("f%d.go", i), ToolClaude, GenTypeChat, base+int64(i), string(raw))
	}
	paths, err := db.TranscriptPathsForPeriod("/r", base-1, base+10)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Errorf("want 2 distinct transcript paths, got %d: %v", len(paths), paths)
	}
}

func TestTranscriptPathsForPeriod_OutsideWindow(t *testing.T) {
	db := openTestDB(t)
	base := int64(1_000_000_000_000)
	raw, _ := json.Marshal(map[string]string{"transcript_path": "/t.jsonl"})
	insertEdit(t, db, "/r", "f.go", ToolClaude, GenTypeChat, base, string(raw))

	paths, err := db.TranscriptPathsForPeriod("/r", base+1, base+100)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Errorf("out-of-window edit should produce 0 paths, got %v", paths)
	}
}

func TestTranscriptPathsForPeriod_NoTranscriptInMeta(t *testing.T) {
	db := openTestDB(t)
	ts := time.Now().UnixNano()
	insertEdit(t, db, "/r", "f.go", ToolClaude, GenTypeChat, ts, `{"source":"hook"}`)
	paths, err := db.TranscriptPathsForPeriod("/r", ts-1, ts+1)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Errorf("raw_meta without transcript_path should return 0 paths, got %v", paths)
	}
}

// ── SessionTranscriptsForPeriod ───────────────────────────────────────────────

func TestSessionTranscriptsForPeriod_Basic(t *testing.T) {
	db := openTestDB(t)
	ts := time.Now().UnixNano()
	raw, _ := json.Marshal(map[string]string{
		"session_id":      "sid1",
		"transcript_path": "/t.jsonl",
		"tool":            "claude",
	})
	insertEdit(t, db, "/r", "f.go", ToolClaude, GenTypeChat, ts, string(raw))
	results, err := db.SessionTranscriptsForPeriod("/r", ts-1, ts+1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].SessionID != "sid1" {
		t.Errorf("want 1 result with sid1, got %v", results)
	}
}

func TestSessionTranscriptsForPeriod_DedupsBySession(t *testing.T) {
	db := openTestDB(t)
	base := time.Now().UnixNano()
	for i := 0; i < 3; i++ {
		raw, _ := json.Marshal(map[string]string{
			"session_id":      "sid-same",
			"transcript_path": "/t.jsonl",
			"tool":            "claude",
		})
		insertEdit(t, db, "/r", fmt.Sprintf("f%d.go", i), ToolClaude, GenTypeChat, base+int64(i), string(raw))
	}
	results, err := db.SessionTranscriptsForPeriod("/r", base-1, base+10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Errorf("same session_id must be deduped: got %d results", len(results))
	}
}

// ── ChatSessionPathsForPeriod ─────────────────────────────────────────────────

func TestChatSessionPathsForPeriod_MatchesRepoAndConfirmedEmpty(t *testing.T) {
	db := openTestDB(t)
	ts := time.Now().UnixNano()
	// Textedit row with matching repo_path — confirms /cs1.jsonl belongs to /r.
	raw1, _ := json.Marshal(map[string]string{"chat_session_path": "/cs1.jsonl", "tool": "copilot"})
	insertEdit(t, db, "/r", "f.go", ToolCopilot, GenTypeChat, ts, string(raw1))
	// Session-response marker for the SAME chat session, recorded with an
	// empty repo_path (the editor event isn't tied to a file) — must be
	// included now that /cs1.jsonl is confirmed for /r.
	raw1b, _ := json.Marshal(map[string]string{"chat_session_path": "/cs1.jsonl", "tool": "copilot"})
	insertEdit(t, db, "", "", ToolCopilot, GenTypeChat, ts+1, string(raw1b))
	// Unrelated chat session recorded with an empty repo_path and never
	// confirmed against /r — must NOT leak into /r's results.
	raw2, _ := json.Marshal(map[string]string{"chat_session_path": "/cs2.jsonl", "tool": "cursor"})
	insertEdit(t, db, "", "", ToolCursor, GenTypeChat, ts+2, string(raw2))
	// Row with different repo_path — must NOT appear.
	raw3, _ := json.Marshal(map[string]string{"chat_session_path": "/cs3.jsonl", "tool": "cursor"})
	insertEdit(t, db, "/other", "g.go", ToolCursor, GenTypeChat, ts+3, string(raw3))

	results, err := db.ChatSessionPathsForPeriod("/r", ts-1, ts+10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Path != "/cs1.jsonl" {
		t.Errorf("want only the confirmed /cs1.jsonl, got %v", results)
	}
}

// ── ResolveSession ────────────────────────────────────────────────────────────

func TestResolveSession_CreatesThenReturnsExisting(t *testing.T) {
	db := openTestDB(t)
	id1, err := db.ResolveSession("/r", "main", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if id1 == "" {
		t.Fatal("ResolveSession returned empty id")
	}
	id2, err := db.ResolveSession("/r", "main", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Errorf("second resolve must return same id: %s vs %s", id1, id2)
	}
}

func TestResolveSession_DifferentBranchDifferentID(t *testing.T) {
	db := openTestDB(t)
	id1, _ := db.ResolveSession("/r", "main", "abc")
	id2, _ := db.ResolveSession("/r", "feature", "abc")
	if id1 == id2 {
		t.Error("different branches must produce different session ids")
	}
}

func TestResolveSession_DifferentBaseSha(t *testing.T) {
	db := openTestDB(t)
	id1, _ := db.ResolveSession("/r", "main", "sha1")
	id2, _ := db.ResolveSession("/r", "main", "sha2")
	if id1 == id2 {
		t.Error("different base_sha must produce different session ids")
	}
}

// ── EditsForFileInSession / EditsForFileAny ───────────────────────────────────

func TestEditsForFileInSession_ScopedToSession(t *testing.T) {
	db := openTestDB(t)
	ts := time.Now().UnixNano()
	sessID, _ := db.ResolveSession("/r", "main", "head1")

	// Edit in session — should appear.
	e := Edit{
		TimestampNanos: ts, RepoPath: "/r", FilePath: "f.go",
		Tool: ToolClaude, Confidence: ConfidenceHigh,
		SessionID: sql.NullString{Valid: true, String: sessID},
	}
	if _, err := db.InsertEdit(e); err != nil {
		t.Fatal(err)
	}

	// Edit without session (legacy NULL row) — should also appear.
	e2 := Edit{
		TimestampNanos: ts + 1, RepoPath: "/r", FilePath: "f.go",
		Tool: ToolClaude, Confidence: ConfidenceHigh,
	}
	if _, err := db.InsertEdit(e2); err != nil {
		t.Fatal(err)
	}

	// Edit in a DIFFERENT session — must NOT appear.
	otherSess, _ := db.ResolveSession("/r", "other", "head2")
	e3 := Edit{
		TimestampNanos: ts + 2, RepoPath: "/r", FilePath: "f.go",
		Tool: ToolClaude, Confidence: ConfidenceHigh,
		SessionID: sql.NullString{Valid: true, String: otherSess},
	}
	if _, err := db.InsertEdit(e3); err != nil {
		t.Fatal(err)
	}

	edits, err := db.EditsForFileInSession("/r", "f.go", sessID)
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 2 {
		t.Errorf("want 2 edits (session + legacy NULL), got %d", len(edits))
	}
}

func TestEditsForFileAny_AllSessions(t *testing.T) {
	db := openTestDB(t)
	ts := time.Now().UnixNano()
	for i := 0; i < 4; i++ {
		if _, err := db.InsertEdit(Edit{
			TimestampNanos: ts + int64(i),
			RepoPath:       "/r", FilePath: "f.go",
			Tool: ToolClaude, Confidence: ConfidenceHigh,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Different file — should not appear.
	if _, err := db.InsertEdit(Edit{
		TimestampNanos: ts, RepoPath: "/r", FilePath: "other.go",
		Tool: ToolClaude, Confidence: ConfidenceHigh,
	}); err != nil {
		t.Fatal(err)
	}
	edits, err := db.EditsForFileAny("/r", "f.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 4 {
		t.Errorf("EditsForFileAny: want 4 edits, got %d", len(edits))
	}
}

// ── RecentPluginEdits ─────────────────────────────────────────────────────────

func TestRecentPluginEdits_FiltersBySourceAndAfterID(t *testing.T) {
	db := openTestDB(t)
	ts := time.Now().UnixNano()
	// Matching source.
	id1 := insertEdit(t, db, "/r", "a.go", ToolClaude, GenTypeChat, ts, `{"source":"vscode_plugin"}`)
	// Non-matching source.
	insertEdit(t, db, "/r", "b.go", ToolClaude, GenTypeChat, ts+1, `{"source":"hook"}`)
	// Matching source but earlier id — used as afterID.
	id3 := insertEdit(t, db, "/r", "c.go", ToolClaude, GenTypeChat, ts+2, `{"source":"vscode_plugin"}`)

	rows, err := db.RecentPluginEdits([]string{"vscode_plugin"}, id1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != id3 {
		t.Errorf("want only id3 (%d), got %v", id3, rows)
	}
}

func TestRecentPluginEdits_EmptySources(t *testing.T) {
	db := openTestDB(t)
	rows, err := db.RecentPluginEdits(nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("nil sources: want 0, got %d", len(rows))
	}
}

func TestRecentPluginEdits_MultipleSources(t *testing.T) {
	db := openTestDB(t)
	ts := time.Now().UnixNano()
	insertEdit(t, db, "/r", "a.go", ToolClaude, GenTypeChat, ts, `{"source":"vscode_plugin"}`)
	insertEdit(t, db, "/r", "b.go", ToolClaude, GenTypeChat, ts+1, `{"source":"intellij_plugin"}`)
	insertEdit(t, db, "/r", "c.go", ToolClaude, GenTypeChat, ts+2, `{"source":"hook"}`)

	rows, err := db.RecentPluginEdits([]string{"vscode_plugin", "intellij_plugin"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Errorf("want 2 rows from two sources, got %d", len(rows))
	}
}

// ── nullableNonEmpty ──────────────────────────────────────────────────────────

func TestNullableNonEmpty(t *testing.T) {
	if nullableNonEmpty("hello") != "hello" {
		t.Error("non-empty string should return the string")
	}
	if nullableNonEmpty("") != nil {
		t.Error("empty string should return nil")
	}
}

func TestNullableString(t *testing.T) {
	if nullableString(sql.NullString{Valid: true, String: "x"}) != "x" {
		t.Error("valid NullString should return string")
	}
	if nullableString(sql.NullString{Valid: false}) != nil {
		t.Error("invalid NullString should return nil")
	}
}

func TestNullableInt(t *testing.T) {
	if nullableInt(sql.NullInt64{Valid: true, Int64: 42}) != int64(42) {
		t.Error("valid NullInt64 should return int64")
	}
	if nullableInt(sql.NullInt64{Valid: false}) != nil {
		t.Error("invalid NullInt64 should return nil")
	}
}

// ── GetFileSnapshot / SetFileSnapshot ──────────────────────────────────────────

func TestFileSnapshot_MissReturnsOkFalse(t *testing.T) {
	db := openTestDB(t)
	content, ok, err := db.GetFileSnapshot("/repo", "main.go")
	if err != nil {
		t.Fatalf("GetFileSnapshot: %v", err)
	}
	if ok {
		t.Error("want ok=false for a file with no cached snapshot")
	}
	if content != "" {
		t.Errorf("want empty content, got %q", content)
	}
}

func TestFileSnapshot_RoundTrip(t *testing.T) {
	db := openTestDB(t)
	if err := db.SetFileSnapshot("/repo", "main.go", "package main\n", 1000); err != nil {
		t.Fatalf("SetFileSnapshot: %v", err)
	}
	content, ok, err := db.GetFileSnapshot("/repo", "main.go")
	if err != nil {
		t.Fatalf("GetFileSnapshot: %v", err)
	}
	if !ok {
		t.Fatal("want ok=true after SetFileSnapshot")
	}
	if content != "package main\n" {
		t.Errorf("content: want %q, got %q", "package main\n", content)
	}
}

func TestFileSnapshot_UpsertOverwrites(t *testing.T) {
	db := openTestDB(t)
	if err := db.SetFileSnapshot("/repo", "main.go", "v1", 1000); err != nil {
		t.Fatalf("SetFileSnapshot v1: %v", err)
	}
	if err := db.SetFileSnapshot("/repo", "main.go", "v2", 2000); err != nil {
		t.Fatalf("SetFileSnapshot v2: %v", err)
	}
	content, ok, err := db.GetFileSnapshot("/repo", "main.go")
	if err != nil {
		t.Fatalf("GetFileSnapshot: %v", err)
	}
	if !ok || content != "v2" {
		t.Errorf("want ok=true, content=%q; got ok=%v, content=%q", "v2", ok, content)
	}
}

func TestFileSnapshot_KeyedByRepoAndFile(t *testing.T) {
	db := openTestDB(t)
	if err := db.SetFileSnapshot("/repo-a", "main.go", "a", 1000); err != nil {
		t.Fatalf("SetFileSnapshot a: %v", err)
	}
	if err := db.SetFileSnapshot("/repo-b", "main.go", "b", 1000); err != nil {
		t.Fatalf("SetFileSnapshot b: %v", err)
	}
	if err := db.SetFileSnapshot("/repo-a", "other.go", "c", 1000); err != nil {
		t.Fatalf("SetFileSnapshot c: %v", err)
	}

	content, _, _ := db.GetFileSnapshot("/repo-a", "main.go")
	if content != "a" {
		t.Errorf("/repo-a main.go: want %q, got %q", "a", content)
	}
	content, _, _ = db.GetFileSnapshot("/repo-b", "main.go")
	if content != "b" {
		t.Errorf("/repo-b main.go: want %q, got %q", "b", content)
	}
	content, _, _ = db.GetFileSnapshot("/repo-a", "other.go")
	if content != "c" {
		t.Errorf("/repo-a other.go: want %q, got %q", "c", content)
	}
}
