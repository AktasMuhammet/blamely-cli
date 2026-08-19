package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// debugHome points ~/.blamely at a temp dir so a test never writes to the
// developer's real hook log.
func debugHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(claudeDebugEnvVar, "")
	return home
}

func readDebugLog(t *testing.T) string {
	t.Helper()
	path, err := ClaudeDebugLogPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatal(err)
	}
	return string(data)
}

func TestClaudeDebug_WritesStepLines(t *testing.T) {
	debugHome(t)

	d := newClaudeDebug()
	if !d.on {
		t.Fatal("logging should be on by default")
	}
	d.logf("parse", "tool_name=%s", debugField("Edit"))
	d.logf("post", "ok")

	body := readDebugLog(t)
	if !strings.Contains(body, "parse") || !strings.Contains(body, `tool_name="Edit"`) {
		t.Errorf("parse step missing from log:\n%s", body)
	}
	if !strings.Contains(body, "post") {
		t.Errorf("post step missing from log:\n%s", body)
	}
	// Every line of one invocation shares the id, which is what makes
	// concurrent hook runs separable in the log.
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d:\n%s", len(lines), body)
	}
	for _, l := range lines {
		if !strings.Contains(l, "["+d.id+"]") {
			t.Errorf("line missing invocation id %q: %s", d.id, l)
		}
	}
}

func TestClaudeDebug_DisabledByEnv(t *testing.T) {
	debugHome(t)

	for _, v := range []string{"0", "false", "off", "no", "OFF"} {
		t.Setenv(claudeDebugEnvVar, v)
		d := newClaudeDebug()
		if d.on {
			t.Errorf("%s=%q should disable logging", claudeDebugEnvVar, v)
		}
		d.logf("parse", "should not be written")
	}
	if body := readDebugLog(t); body != "" {
		t.Errorf("log written while disabled:\n%s", body)
	}
}

func TestClaudeDebug_RotatesAtCap(t *testing.T) {
	home := debugHome(t)

	path := filepath.Join(home, ".blamely", claudeDebugLogName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// Seed a log that has already outgrown the cap.
	if err := os.WriteFile(path, []byte(strings.Repeat("x", claudeDebugMaxBytes+1)), 0o644); err != nil {
		t.Fatal(err)
	}

	newClaudeDebug().logf("post", "after rotation")

	if _, err := os.Stat(path + claudeDebugRotatedSuffix); err != nil {
		t.Fatalf("previous generation not kept: %v", err)
	}
	body := readDebugLog(t)
	if strings.Contains(body, "xxxx") {
		t.Error("live log still holds the pre-rotation content")
	}
	if !strings.Contains(body, "after rotation") {
		t.Errorf("new line missing after rotation:\n%s", body)
	}
}

func TestDebugField_CollapsesAndTruncates(t *testing.T) {
	if got := debugField("a\n  b\tc"); got != `"a b c"` {
		t.Errorf("whitespace not collapsed: %s", got)
	}
	long := debugField(strings.Repeat("y", claudeDebugMaxField+50))
	if !strings.Contains(long, "truncated") {
		t.Errorf("oversized value not truncated: len=%d", len(long))
	}
	if len(long) > claudeDebugMaxField+40 {
		t.Errorf("truncated value still too long: %d", len(long))
	}
}

func TestDebugTokens_NilRendersDash(t *testing.T) {
	if got := debugTokens(nil); got != "-" {
		t.Errorf("nil token count = %q, want %q", got, "-")
	}
	n := int64(42)
	if got := debugTokens(&n); got != "42" {
		t.Errorf("token count = %q, want %q", got, "42")
	}
}

// TestRecordClaudeFromStdin_LogsRejectedPayload covers the case the log exists
// for: a payload the hook could not parse. Nothing reaches the database, so the
// log is the only trace of the request.
func TestRecordClaudeFromStdin_LogsRejectedPayload(t *testing.T) {
	debugHome(t)

	err := RecordClaudeFromStdin(strings.NewReader("{not json"))
	if err == nil {
		t.Fatal("want a parse error")
	}

	body := readDebugLog(t)
	if !strings.Contains(body, "REJECTED") {
		t.Errorf("parse rejection not logged:\n%s", body)
	}
	if !strings.Contains(body, "payload") || !strings.Contains(body, "{not json") {
		t.Errorf("raw payload not logged:\n%s", body)
	}
}

// TestRecordClaudeFromStdin_LogsNoFilePathBranch covers a well-formed payload
// from a tool that yields no file edit: the run is a legitimate no-op, and the
// log has to say so rather than going silent.
func TestRecordClaudeFromStdin_LogsNoFilePathBranch(t *testing.T) {
	debugHome(t)

	payload := `{"session_id":"s1","cwd":"/tmp/nowhere","tool_name":"Read","tool_input":{}}`
	if err := RecordClaudeFromStdin(strings.NewReader(payload)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	body := readDebugLog(t)
	for _, want := range []string{"parse", `tool_name="Read"`, "host=claude", "extract", "branch"} {
		if !strings.Contains(body, want) {
			t.Errorf("log missing %q:\n%s", want, body)
		}
	}
	if !strings.Contains(body, "nothing recorded") {
		t.Errorf("no-op branch not explained:\n%s", body)
	}
}

// TestDaemonEndpointDesc_NoDaemon asserts the silent-drop case is named
// explicitly: postToDaemon returns nil when no daemon is running, so without
// this line a dropped edit looks identical to a successful one.
func TestDaemonEndpointDesc_NoDaemon(t *testing.T) {
	debugHome(t)

	if got := daemonEndpointDesc(); !strings.Contains(got, "UNREACHABLE") {
		t.Errorf("endpoint description = %q, want it to flag the dropped edit", got)
	}
}

func TestLastLinesOfFiles_ReadsRotatedThenLive(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "log")
	rotated := live + claudeDebugRotatedSuffix
	if err := os.WriteFile(rotated, []byte("old1\nold2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(live, []byte("new1\nnew2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := lastLinesOfFiles(10, rotated, live)
	want := []string{"old1", "old2", "new1", "new2"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}

	// The cap keeps the NEWEST lines, not the oldest.
	if got := lastLinesOfFiles(2, rotated, live); strings.Join(got, ",") != "new1,new2" {
		t.Errorf("capped read = %v, want the two newest lines", got)
	}

	// A missing rotated generation is not an error.
	if got := lastLinesOfFiles(10, filepath.Join(dir, "absent"), live); len(got) != 2 {
		t.Errorf("missing file not skipped: %v", got)
	}
}
