package install

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestIsLauncherEntry(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"blamely_v1.8.1_windows_amd64/blamelyw.exe", true},
		{"blamely_windows_arm64/blamelyw.exe", true}, // rolling channel: no tag in the dir
		{"blamelyw.exe", true},
		{"blamely_v1.8.1_windows_amd64/blamely.exe", false},
		{"blamelyw", false},
		{"blamelyw.exe.old-1700000000", false}, // a renamed-aside copy is not the launcher
	}
	for _, c := range cases {
		if got := isLauncherEntry(c.name); got != c.want {
			t.Errorf("isLauncherEntry(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

// buildZipEntries writes a zip with several entries, which the release archives
// have and buildZip (one entry) can't express.
func buildZipEntries(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// The launcher rides along in the Windows release .zip, and extractBinary must
// stage it next to the binary — that co-location is the whole mechanism by which
// CopyBinary later installs the pair (and the tasks find blamelyw.exe next to
// blamely.exe). Runs on any host: the zip path is host-independent.
func TestExtractBinary_StagesLauncherSidecar(t *testing.T) {
	src := filepath.Join(t.TempDir(), "release.zip")
	inner := "blamely_v1.8.1_windows_amd64/"
	if err := os.WriteFile(src, buildZipEntries(t, map[string]string{
		inner + "blamely.exe":  "binary-bytes",
		inner + "blamelyw.exe": "launcher-bytes",
	}), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	got, err := extractBinary(src, dest)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(got); err != nil || string(b) != "binary-bytes" {
		t.Fatalf("binary = %q, %v", b, err)
	}
	b, err := os.ReadFile(filepath.Join(dest, launcherName))
	if err != nil {
		t.Fatalf("launcher not staged next to the binary: %v", err)
	}
	if string(b) != "launcher-bytes" {
		t.Errorf("launcher = %q, want launcher-bytes", b)
	}
}

// An archive from before the launcher existed must still update cleanly — the
// autostart entries fall back to blamely.exe, which is worse UX but a working
// install, and refusing the update would be far worse.
func TestExtractBinary_LauncherIsOptional(t *testing.T) {
	src := filepath.Join(t.TempDir(), "release.zip")
	if err := os.WriteFile(src, buildZipEntries(t, map[string]string{
		"blamely_windows_amd64/blamely.exe": "binary-bytes",
	}), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if _, err := extractBinary(src, dest); err != nil {
		t.Fatalf("an archive without the launcher must still extract: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, launcherName)); !os.IsNotExist(err) {
		t.Errorf("stat launcher = %v, want not-exist", err)
	}
}
