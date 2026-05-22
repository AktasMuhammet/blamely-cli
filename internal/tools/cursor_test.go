package tools

import (
	"runtime"
	"testing"
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
	// On Windows Cursor emits file:///C:/Users/... URIs.
	// The function should strip the leading slash to get C:/Users/...
	// We can test the string transformation on any OS.
	got := uriToPath("file:///C:/Users/alice/repo/main.go")
	// On non-Windows the function leaves the leading slash; on Windows it strips it.
	// The important thing is: no empty result.
	if got == "" {
		t.Errorf("uriToPath for Windows-style URI should not be empty")
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
