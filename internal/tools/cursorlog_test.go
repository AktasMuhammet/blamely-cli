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
