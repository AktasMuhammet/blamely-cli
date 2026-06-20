package install

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCopyBinary_UpgradeReplacesAndReapsBackups covers the Windows in-place
// upgrade path: a new binary must replace the existing one, and stale
// blamely.exe.old-* copies left by a previous locked upgrade must be reaped so
// ~/.blamely/bin doesn't accumulate them.
func TestCopyBinary_UpgradeReplacesAndReapsBackups(t *testing.T) {
	home := fakeHomeDir(t)

	dst, err := InstalledBinaryPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	// An existing (old) install plus a stale backup from a prior locked upgrade.
	if err := os.WriteFile(dst, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := dst + ".old-123456789"
	if err := os.WriteFile(stale, []byte("stale"), 0o755); err != nil {
		t.Fatal(err)
	}

	// New binary to install, from a different location (src != dst).
	src := filepath.Join(home, "download", "blamely-new")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("new-binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := CopyBinary(src)
	if err != nil {
		t.Fatalf("CopyBinary: %v", err)
	}
	if got != dst {
		t.Fatalf("CopyBinary returned %q, want %q", got, dst)
	}

	// The stable binary now holds the new contents.
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new-binary" {
		t.Errorf("installed binary = %q, want %q (old version not replaced)", data, "new-binary")
	}

	// The stale .old-* backup was reaped.
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale backup %s still present (bin dir not cleaned)", stale)
	}
}

// TestPlaceBinary_RenameAsideFallback exercises the fallback used on Windows when
// the destination can't be replaced directly: placeBinary moves the blocking dst
// aside and still lands the new file. Simulated portably by making dst a
// non-empty directory, which makes the direct rename fail on every OS.
func TestPlaceBinary_RenameAsideFallback(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "blamely.exe")

	// dst is a non-empty directory → os.Rename(tmp, dst) fails directly.
	if err := os.MkdirAll(filepath.Join(dst, "locked"), 0o755); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(dir, "staged")
	if err := os.WriteFile(tmp, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := placeBinary(tmp, dst); err != nil {
		t.Fatalf("placeBinary: %v", err)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("dst not a regular file after placeBinary: %v", err)
	}
	if string(data) != "new" {
		t.Errorf("dst = %q, want %q", data, "new")
	}
	// The blocking dst was renamed aside, not lost.
	asides, _ := filepath.Glob(dst + ".old-*")
	if len(asides) != 1 {
		t.Errorf("expected 1 renamed-aside copy, found %d", len(asides))
	}
}
