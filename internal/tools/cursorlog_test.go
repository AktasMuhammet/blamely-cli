package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestExtractCursorApplyPath_ComposerApply(t *testing.T) {
	line := `[2026-05-21 12:00:01.234] [info] ComposerApply: applying changes to file:///Users/alice/repo/main.go`
	path, ok := extractCursorApplyPath(line)
	if !ok {
		t.Fatal("expected ok=true for ComposerApply line")
	}
	if path != "/Users/alice/repo/main.go" {
		t.Errorf("want /Users/alice/repo/main.go, got %q", path)
	}
}

func TestExtractCursorApplyPath_AgentEdit(t *testing.T) {
	line := `{"level":"info","msg":"agentEdit file:///home/bob/project/src/util.py","ts":1234567890}`
	path, ok := extractCursorApplyPath(line)
	if !ok {
		t.Fatal("expected ok=true for agentEdit line")
	}
	if path != "/home/bob/project/src/util.py" {
		t.Errorf("want /home/bob/project/src/util.py, got %q", path)
	}
}

func TestExtractCursorApplyPath_NoApplyKeyword(t *testing.T) {
	line := `[info] Reading file /Users/alice/repo/main.go`
	_, ok := extractCursorApplyPath(line)
	if ok {
		t.Error("expected ok=false for line without apply keyword")
	}
}

func TestExtractCursorApplyPath_BareAbsPath(t *testing.T) {
	line := `[info] composerapply /Users/alice/my-project/x.go done`
	path, ok := extractCursorApplyPath(line)
	if !ok {
		t.Fatal("expected ok=true for bare absolute path with composerapply keyword")
	}
	if path != "/Users/alice/my-project/x.go" {
		t.Errorf("got %q", path)
	}
}

func TestExtractCursorApplyPath_EmptyLine(t *testing.T) {
	_, ok := extractCursorApplyPath("")
	if ok {
		t.Error("expected ok=false for empty line")
	}
}

// TestExtractCursorApplyPath_BareComposerWordNoMatch is a regression test
// for the over-broad keyword filter that earlier matched ANY line
// containing the substring "composer". That false-positives on benign log
// lines like the one below and caused human typing in Cursor to be
// attributed as `cursor/chat`. The filter must now require a more specific
// apply/edit marker.
func TestExtractCursorApplyPath_BareComposerWordNoMatch(t *testing.T) {
	for _, line := range []string{
		`[info] composer session started for file:///Users/alice/main.go`,
		`[debug] composer panel: opened`,
		`{"msg":"composer settings updated","file":"/Users/alice/x.go"}`,
	} {
		if _, ok := extractCursorApplyPath(line); ok {
			t.Errorf("expected ok=false for benign composer line: %q", line)
		}
	}
}

// TestExtractCursorApplyPath_ApplyEditNoMatch is a regression test for the
// over-broad "applyedit" keyword that was previously included in the filter.
// VS Code (and therefore Cursor) emits applyEdit for Tab completions,
// formatter rewrites, auto-imports, and other non-AI edits. Matching it
// caused two bugs: (1) Tab-completion log lines (written after the file
// save) produced newer whole-file cursor/chat rows that overrode humanedit
// rows for manually-typed lines in the same session; (2) all such events
// showed gen_type="chat" instead of "completion". The keyword must NOT match.
func TestExtractCursorApplyPath_ApplyEditNoMatch(t *testing.T) {
	for _, line := range []string{
		`[info] applyEdit to file:///Users/alice/main.go`,
		`{"msg":"applyedit","file":"/Users/alice/x.go"}`,
		`[debug] applyEdit: tab completion accepted file:///Users/alice/y.go`,
	} {
		if _, ok := extractCursorApplyPath(line); ok {
			t.Errorf("expected ok=false for applyedit line (too broad): %q", line)
		}
	}
}

func TestExtractFilePath_FileURI(t *testing.T) {
	line := `see file:///tmp/foo.go for details`
	path, ok := extractFilePath(line)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if path != "/tmp/foo.go" {
		t.Errorf("want /tmp/foo.go, got %q", path)
	}
}

func TestExtractFilePath_AbsPath(t *testing.T) {
	line := `writing /Users/alice/project/main.go`
	path, ok := extractFilePath(line)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if path != "/Users/alice/project/main.go" {
		t.Errorf("want /Users/alice/project/main.go, got %q", path)
	}
}

func TestExtractFilePath_NoPath(t *testing.T) {
	_, ok := extractFilePath("no file path here")
	if ok {
		t.Error("expected ok=false when no path present")
	}
}

func TestExtractFilePath_StopsAtDelimiter(t *testing.T) {
	line := `path="file:///home/user/x.go" other=stuff`
	path, ok := extractFilePath(line)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if path != "/home/user/x.go" {
		t.Errorf("should stop at closing quote, got %q", path)
	}
}

// ---- isCursorTabLog ----

func TestIsCursorTabLog_TabLogFile(t *testing.T) {
	// The Tab log file is named "Cursor Tab.log" inside an extension dir.
	path := "/Users/alice/Library/Application Support/Cursor/logs/20260525/exthost1/anysphere.cursor-always-local/Cursor Tab.log"
	if !isCursorTabLog(path) {
		t.Error("expected isCursorTabLog=true for 'Cursor Tab.log'")
	}
}

func TestIsCursorTabLog_CursorAlwaysLocalDir(t *testing.T) {
	// Any file inside a cursor-always-local extension dir should match.
	path := "/logs/exthost1/anysphere.cursor-always-local/someother.log"
	if !isCursorTabLog(path) {
		t.Error("expected isCursorTabLog=true for file inside cursor-always-local dir")
	}
}

func TestIsCursorTabLog_ComposerLog(t *testing.T) {
	// A normal Composer extension log must NOT match.
	path := "/logs/exthost1/anysphere.cursor/extension-host.log"
	if isCursorTabLog(path) {
		t.Error("expected isCursorTabLog=false for regular Cursor extension log")
	}
}

// ---- parseCursorTabHunkHeader ----

func TestParseCursorTabHunkHeader_WithLineNum(t *testing.T) {
	path, start, ok := parseCursorTabHunkHeader("@@ src/main/java/com/example/SecurityConfig.java:72")
	if !ok {
		t.Fatal("expected ok=true for @@ line with line number")
	}
	if path != "src/main/java/com/example/SecurityConfig.java" {
		t.Errorf("want stripped path without :72, got %q", path)
	}
	if start != 72 {
		t.Errorf("want start line 72, got %d", start)
	}
}

func TestParseCursorTabHunkHeader_WithoutLineNum(t *testing.T) {
	path, start, ok := parseCursorTabHunkHeader("@@ src/main/main.go")
	if !ok || path != "src/main/main.go" {
		t.Fatalf("want src/main/main.go ok, got %q ok=%v", path, ok)
	}
	if start != 1 {
		t.Errorf("want default start line 1, got %d", start)
	}
}

func TestParseCursorTabHunkHeader_NoAtAt(t *testing.T) {
	if _, _, ok := parseCursorTabHunkHeader("CPP Request Log: src/main/main.go"); ok {
		t.Error("expected ok=false for line that doesn't start with @@")
	}
}

func TestParseCursorTabHunkHeader_EmptyAfterAtat(t *testing.T) {
	if _, _, ok := parseCursorTabHunkHeader("@@"); ok {
		t.Error("expected ok=false for bare @@")
	}
}

// ---- splitCursorTabDiffLine ----

func TestSplitCursorTabDiffLine(t *testing.T) {
	cases := []struct {
		line       string
		wantMarker byte
		wantText   string
		wantOK     bool
	}{
		{"+|      Forgot your password?", '+', "      Forgot your password?", true},
		{"-|      Don't have an account?", '-', "      Don't have an account?", true},
		{" |context line", ' ', "context line", true},
		{"+no pipe still ok", '+', "no pipe still ok", true},
		{"@@ login.html:1", 0, "", false},
		{"", 0, "", false},
	}
	for _, c := range cases {
		m, txt, ok := splitCursorTabDiffLine(c.line)
		if ok != c.wantOK || m != c.wantMarker || txt != c.wantText {
			t.Errorf("splitCursorTabDiffLine(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.line, m, txt, ok, c.wantMarker, c.wantText, c.wantOK)
		}
	}
}

// ---- isCursorTabBlockEnd ----

func TestIsCursorTabBlockEnd(t *testing.T) {
	if !isCursorTabBlockEnd("2026-06-13 10:55:33.930 [info] =======>Debug stream time 201") {
		t.Error("a timestamped Debug line should end the block")
	}
	if !isCursorTabBlockEnd("2026-06-13 10:55:34.991 [info] CURSOR LOG: requestId abc") {
		t.Error("the next timestamped log entry should end the block")
	}
	if isCursorTabBlockEnd("+|      Forgot your password?") {
		t.Error("a diff body line must NOT end the block")
	}
	if isCursorTabBlockEnd("@@ login.html:134") {
		t.Error("a hunk header must NOT end the block")
	}
}

// ---- parseCursorTabBlock (real Cursor Tab.log diff format) ----

func TestParseCursorTabBlock_RealFormat(t *testing.T) {
	// Exactly the body Cursor writes after "=======>Model output" (verified
	// against a real Cursor Tab.log): an @@ header then -|/+| lines.
	block := []string{
		"@@ login.html:134",
		`-|      Don't have an account? <a href="register.html">Sign up</a>`,
		`+|      Forgot your password? <a href="forgot-password.html">Reset password</a>`,
	}
	sug := parseCursorTabBlock(block)
	if sug.RelPath != "login.html" {
		t.Fatalf("RelPath = %q, want login.html", sug.RelPath)
	}
	if len(sug.Added) != 1 {
		t.Fatalf("Added = %d, want 1", len(sug.Added))
	}
	if sug.Added[0].Start != 134 || sug.Added[0].End != 134 {
		t.Errorf("added line = %d-%d, want 134-134", sug.Added[0].Start, sug.Added[0].End)
	}
	// content_sha must be of the text WITHOUT the "+|" marker, so it can match
	// the committed line.
	wantText := `      Forgot your password? <a href="forgot-password.html">Reset password</a>`
	if sug.Added[0].ContentSHA != sha256Hex([]byte(wantText)) {
		t.Errorf("added content_sha doesn't match the un-prefixed line text")
	}
	if len(sug.Removed) != 1 {
		t.Fatalf("Removed = %d, want 1", len(sug.Removed))
	}
	wantOld := `      Don't have an account? <a href="register.html">Sign up</a>`
	if sug.Removed[0].ContentSHA != sha256Hex([]byte(wantOld)) {
		t.Errorf("removed content_sha doesn't match the un-prefixed old line text")
	}
}

func TestParseCursorTabBlock_AddedLineNumbersAdvance(t *testing.T) {
	// Two added lines after one context line: numbering starts at the hunk line
	// and advances past context, but a removed line does NOT advance it.
	block := []string{
		"@@ main.go:10",
		" |func main() {", // context → line 10
		`-|	old()`,        // removed → does not advance
		`+|	first()`,      // added  → line 11
		`+|	second()`,     // added  → line 12
	}
	sug := parseCursorTabBlock(block)
	if len(sug.Added) != 2 {
		t.Fatalf("Added = %d, want 2", len(sug.Added))
	}
	if sug.Added[0].Start != 11 || sug.Added[1].Start != 12 {
		t.Errorf("added lines = %d,%d, want 11,12", sug.Added[0].Start, sug.Added[1].Start)
	}
}

func TestParseCursorTabBlock_EmptyOutput(t *testing.T) {
	// Most Tab generations have an empty model-output block; it must yield no
	// path and no lines so the caller falls back to the session marker.
	sug := parseCursorTabBlock(nil)
	if sug.RelPath != "" || len(sug.Added) != 0 || len(sug.Removed) != 0 {
		t.Errorf("empty block should parse to nothing, got %+v", sug)
	}
}

// ---- resolveCursorTabFile ----

func TestResolveCursorTabFile(t *testing.T) {
	root := t.TempDir()
	// relative path, single root → joined unconditionally
	got, ok := resolveCursorTabFile("login.html", []string{root})
	if !ok || got != filepath.Join(root, "login.html") {
		t.Errorf("single-root join = %q ok=%v", got, ok)
	}
	// absolute path → returned as-is
	abs := filepath.Join(root, "x.go")
	if got, ok := resolveCursorTabFile(abs, nil); !ok || got != abs {
		t.Errorf("absolute path should pass through, got %q ok=%v", got, ok)
	}
	// empty → not ok
	if _, ok := resolveCursorTabFile("", []string{root}); ok {
		t.Error("empty relPath should not resolve")
	}
	// multiple roots, prefer the one whose dir exists
	other := t.TempDir()
	got, ok = resolveCursorTabFile("page.html", []string{filepath.Join(other, "missing"), root})
	if !ok || got != filepath.Join(root, "page.html") {
		t.Errorf("multi-root resolution = %q ok=%v, want under %s", got, ok, root)
	}
}

// TestEmitCursorTabSuggestion_EndToEnd drives the full parse → resolve → emit
// path: a real-format Tab block plus a workspace root resolves to a concrete
// cursor/completion event carrying per-line content_sha for the accepted text.
func TestEmitCursorTabSuggestion_EndToEnd(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	// stable content hashes across OSes (Windows git defaults to autocrlf=true)
	_ = exec.Command("git", "-C", root, "config", "core.autocrlf", "false").Run()
	line := `      Forgot your password? <a href="forgot-password.html">Reset password</a>`
	if err := os.WriteFile(filepath.Join(root, "login.html"), []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	block := []string{
		"@@ login.html:134",
		`-|      Don't have an account? <a href="register.html">Sign up</a>`,
		"+|" + line,
	}
	sink := &captureSink{}
	emitCursorTabSuggestion(block, []string{root}, map[string]bool{}, sink)

	if len(sink.events) != 1 {
		t.Fatalf("want 1 event, got %d", len(sink.events))
	}
	ev := sink.events[0]
	if ev.Tool != "cursor" || ev.GenType != "completion" {
		t.Errorf("tool/gen = %q/%q, want cursor/completion", ev.Tool, ev.GenType)
	}
	if ev.FilePath != "login.html" {
		t.Errorf("FilePath = %q, want login.html", ev.FilePath)
	}
	if len(ev.Lines) != 1 || ev.Lines[0].Start != 134 {
		t.Fatalf("Lines = %+v, want one range at line 134", ev.Lines)
	}
	if ev.Lines[0].ContentSHA != sha256Hex([]byte(line)) {
		t.Error("emitted content_sha must match the accepted line text")
	}
	if len(ev.RemovedLines) != 1 {
		t.Errorf("want 1 removed-line hash, got %d", len(ev.RemovedLines))
	}

	// De-dupe: the same suggestion re-logged must not emit again.
	seen := map[string]bool{}
	emitCursorTabSuggestion(block, []string{root}, seen, sink)
	emitCursorTabSuggestion(block, []string{root}, seen, sink)
	if len(sink.events) != 2 {
		t.Errorf("re-shown suggestion should emit once, got %d total events", len(sink.events))
	}
}

// TestEmitCursorTabSuggestion_UnresolvedFallsBackToMarker verifies that a block
// whose path can't be resolved still emits the low-confidence "Tab active"
// marker (no file/lines) rather than dropping the signal.
func TestEmitCursorTabSuggestion_UnresolvedFallsBackToMarker(t *testing.T) {
	block := []string{"@@ login.html:1", "+|something"}
	sink := &captureSink{}
	emitCursorTabSuggestion(block, nil, map[string]bool{}, sink) // no roots → unresolved
	if len(sink.events) != 1 {
		t.Fatalf("want 1 marker event, got %d", len(sink.events))
	}
	if ev := sink.events[0]; ev.FilePath != "" || len(ev.Lines) != 0 || ev.GenType != "completion" {
		t.Errorf("fallback should be a file-less completion marker, got %+v", ev)
	}
}

func TestCursorWindowWorkspaceRoots(t *testing.T) {
	win := t.TempDir()
	exthost := filepath.Join(win, "exthost")
	tabDir := filepath.Join(exthost, "anysphere.cursor-always-local")
	if err := os.MkdirAll(tabDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// the workspace cwd must be a real directory (the parser stats it)
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(exthost, "exthost.log"),
		[]byte(`ExtHostSearch folderQueries=[{"cwd":"`+ws+`","x":1}]`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tabLog := filepath.Join(tabDir, "Cursor Tab.log")
	roots := cursorWindowWorkspaceRoots(tabLog)
	if len(roots) != 1 || roots[0] != ws {
		t.Fatalf("cursorWindowWorkspaceRoots = %v, want [%s]", roots, ws)
	}
	// no exthost.log → empty, no panic
	if r := cursorWindowWorkspaceRoots(filepath.Join(t.TempDir(), "x", "y", "Cursor Tab.log")); len(r) != 0 {
		t.Errorf("missing exthost.log should yield no roots, got %v", r)
	}
}
