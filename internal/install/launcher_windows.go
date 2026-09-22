//go:build windows

package install

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// launcherPath returns where the windowless launcher belongs relative to
// binaryPath — right next to it, which is how cmd/blamelyw finds blamely.exe in
// the other direction — and whether it is actually there.
//
// Every caller treats a missing launcher as "use blamely.exe directly", so a dev
// build (`go build ./cmd/blamely`), an install from an archive that predates the
// launcher, or an upgrade whose sidecar copy failed all keep working exactly as
// before — with the old flashing console, but working.
func launcherPath(binaryPath string) (string, bool) {
	p := filepath.Join(filepath.Dir(binaryPath), launcherName)
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return p, false
	}
	return p, true
}

// syncLauncher copies the launcher that shipped next to srcBin into the
// installed bin directory, next to dstBin. Called from CopyBinary, so every path
// that installs or updates the binary — the Inno installer running
// `{app}\blamely.exe install`, the bootstrap script, `blamely update` unpacking a
// release archive — brings the launcher along with it.
//
// Best-effort by contract: the caller ignores the error because the autostart
// entries fall back to blamely.exe when the launcher is absent. A nil return with
// nothing copied is the normal case on a dev machine, where no launcher was
// built.
func syncLauncher(srcBin, dstBin string) error {
	src, ok := launcherPath(srcBin)
	if !ok {
		return nil // nothing shipped alongside this binary
	}
	dst, _ := launcherPath(dstBin)
	if same, _ := sameFile(src, dst); same {
		return nil // already the installed copy (re-running install in place)
	}
	// Reap copies renamed aside by an earlier locked replace, exactly as the
	// main binary's install does.
	cleanStaleBinaryBackups(dst)

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(dst), err)
	}
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".blamelyw-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return fmt.Errorf("copy: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// placeBinary, not os.Rename: the launcher lives for milliseconds at a time,
	// but a keepalive firing at exactly the wrong moment can still hold the image
	// locked, and Windows only lets a locked exe be renamed aside — which is
	// precisely what placeBinary falls back to.
	if err := placeBinary(tmpPath, dst); err != nil {
		return err
	}
	return nil
}
