package tools

import (
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

// ---- extractCursorTabPath ----

func TestExtractCursorTabPath_WithLineNum(t *testing.T) {
	// Typical Cursor Tab diff header includes a colon+line-number suffix.
	line := "@@ src/main/java/com/example/SecurityConfig.java:72"
	path, ok := extractCursorTabPath(line)
	if !ok {
		t.Fatal("expected ok=true for @@ line with line number")
	}
	if path != "src/main/java/com/example/SecurityConfig.java" {
		t.Errorf("want stripped path without :72, got %q", path)
	}
}

func TestExtractCursorTabPath_WithoutLineNum(t *testing.T) {
	line := "@@ src/main/main.go"
	path, ok := extractCursorTabPath(line)
	if !ok {
		t.Fatal("expected ok=true for @@ line without line number")
	}
	if path != "src/main/main.go" {
		t.Errorf("want %q, got %q", "src/main/main.go", path)
	}
}

func TestExtractCursorTabPath_NoAtAt(t *testing.T) {
	line := "CPP Request Log: something src/main/main.go"
	_, ok := extractCursorTabPath(line)
	if ok {
		t.Error("expected ok=false for line that doesn't start with @@")
	}
}

func TestExtractCursorTabPath_EmptyAfterAtat(t *testing.T) {
	line := "@@"
	_, ok := extractCursorTabPath(line)
	if ok {
		t.Error("expected ok=false for bare @@ with nothing after it")
	}
}
