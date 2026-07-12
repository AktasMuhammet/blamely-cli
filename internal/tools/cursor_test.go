package tools

import (
	"runtime"
	"testing"
	"time"
)

func TestUriToPath_UnixFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix-path test not applicable on Windows")
	}
	got := uriToPath("file:///Users/alice/repo/main.go")
	want := "/Users/alice/repo/main.go"
	if got != want {
		t.Errorf("uriToPath: want %q, got %q", want, got)
	}
}

func TestUriToPath_SpacePercent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	got := uriToPath("file:///Users/alice/my%20repo/main.go")
	want := "/Users/alice/my repo/main.go"
	if got != want {
		t.Errorf("uriToPath: want %q, got %q", want, got)
	}
}

func TestUriToPath_NonFileScheme(t *testing.T) {
	cases := []string{
		"untitled:Untitled-1",
		"http://example.com/foo",
		"vscode://some/ext",
		"",
	}
	for _, uri := range cases {
		got := uriToPath(uri)
		if got != "" {
			t.Errorf("uriToPath(%q): want empty, got %q", uri, got)
		}
	}
}

func TestUriToPath_WindowsPath(t *testing.T) {
	// On Windows Cursor emits file:///C:/Users/... URIs — often with a
	// lowercase drive letter and a percent-encoded colon (c%3A). The drive
	// strip is unconditional (a Unix path never starts with "/<letter>:/"),
	// so the transformation is testable on any OS.
	cases := []struct{ uri, want string }{
		{"file:///C:/Users/alice/repo/main.go", "C:/Users/alice/repo/main.go"},
		{"file:///c%3A/Users/alice/repo/main.go", "c:/Users/alice/repo/main.go"},
		{"file:///c:/Users/alice/my%20repo/main.go", "c:/Users/alice/my repo/main.go"},
	}
	for _, c := range cases {
		if got := uriToPath(c.uri); got != c.want {
			t.Errorf("uriToPath(%q): want %q, got %q", c.uri, c.want, got)
		}
	}
}

func TestStripWindowsDriveSlash(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/c:/Users/x/f.go", "c:/Users/x/f.go"},
		{"/C:\\Users\\x\\f.go", "C:\\Users\\x\\f.go"},
		{"/Users/alice/repo/main.go", "/Users/alice/repo/main.go"}, // Unix untouched
		{"/1:/oddity", "/1:/oddity"},                               // not a drive letter
		{"c:/already/stripped", "c:/already/stripped"},
	}
	for _, c := range cases {
		if got := stripWindowsDriveSlash(c.in); got != c.want {
			t.Errorf("stripWindowsDriveSlash(%q): want %q, got %q", c.in, c.want, got)
		}
	}
}

// TestCursorWatcherEmit_NeverWritesAttribution proves that CursorWatcher.emit
// no longer produces any sink events for a File History snapshot.
//
// History snapshots fire on every Cursor save — manual typing included —
// so any emission here ends up attributing human-typed code to
// `cursor/chat`. The function is intentionally a no-op now; this test
// pins that contract so we don't accidentally reintroduce the bug.
func TestCursorWatcherEmit_NeverWritesAttribution(t *testing.T) {
	w := &CursorWatcher{}
	sink := &mockSink{}
	manifest := &cursorEntries{Resource: "file:///tmp/whatever.go"}
	w.emit(manifest, "/tmp/whatever.go/snap", time.Now(), sink)
	if got := len(sink.events); got != 0 {
		t.Errorf("emit must record zero events; got %d", got)
	}
}

func TestBytesTrimNewline(t *testing.T) {
	cases := []struct {
		in   []byte
		want []byte
	}{
		{[]byte("hello\n"), []byte("hello")},
		{[]byte("hello\r\n"), []byte("hello")},
		{[]byte("hello"), []byte("hello")},
		{[]byte("\n"), []byte{}},
		{[]byte{}, []byte{}},
	}
	for _, c := range cases {
		got := bytesTrimNewline(c.in)
		if string(got) != string(c.want) {
			t.Errorf("bytesTrimNewline(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
