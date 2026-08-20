package install

import (
	"bytes"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestChildOutput_DropsUnusableFile(t *testing.T) {
	// A nil *os.File is what a console-less process's std handle degrades to
	// once usableStdHandle rejects it. It must never reach os/exec, which would
	// hand CreateProcess a std handle it refuses.
	if got := childOutput((*os.File)(nil)); got != io.Discard {
		t.Errorf("nil *os.File = %T, want io.Discard", got)
	}
	if got := childOutput(nil); got != io.Discard {
		t.Errorf("nil writer = %T, want io.Discard", got)
	}
}

func TestChildOutput_PassesOrdinaryWriterThrough(t *testing.T) {
	var buf bytes.Buffer
	if got := childOutput(&buf); got != io.Writer(&buf) {
		t.Errorf("ordinary writer was replaced with %T", got)
	}
	// A usable *os.File stays inherited, so an interactive `blamely update`
	// keeps writing straight to its console. A real temp file rather than
	// os.Stdout: on Windows the test binary may itself be running without
	// standard handles, and then os.Stdout is exactly the unusable case the
	// other test covers.
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if got := childOutput(f); got != io.Writer(f) {
		t.Errorf("usable *os.File was replaced with %T", got)
	}
}

// TestRunInstaller_GivesChildNoStdin is the regression guard for the Windows
// auto-update failure: runInstaller used to assign os.Stdin/os.Stdout/os.Stderr
// to the child, so a daemon started with CREATE_NO_WINDOW passed on std handles
// CreateProcess rejected with ERROR_NOT_SUPPORTED. The child must get an empty
// stdin and send its output to the writer we asked for.
func TestRunInstaller_GivesChildNoStdin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh")
	}
	var out bytes.Buffer
	// `cat` drains stdin: with no stdin it sees EOF at once instead of blocking.
	if err := runInstaller("/bin/sh", &out, "-c", "cat; echo done-$?"); err != nil {
		t.Fatalf("runInstaller: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "done-0" {
		t.Errorf("child output = %q, want %q (a non-empty or blocking stdin would change this)", got, "done-0")
	}
}

func TestRunInstaller_MergesStderrIntoOut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh")
	}
	var out bytes.Buffer
	err := runInstaller("/bin/sh", &out, "-c", "echo to-stderr >&2; exit 3")
	if err == nil {
		t.Fatal("want the child's non-zero exit reported")
	}
	// The installer's diagnosis usually arrives on stderr; losing it is what
	// left the daemon's auto-update failure with nothing but an exit status.
	if !strings.Contains(out.String(), "to-stderr") {
		t.Errorf("stderr missing from captured output: %q", out.String())
	}
}

func TestUsableStdHandle_NilIsUnusable(t *testing.T) {
	if usableStdHandle(nil) {
		t.Error("a nil file must never be offered to a child process")
	}
}
