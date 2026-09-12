package install

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// stubInstaller replaces the hand-off for one test and restores the real one
// afterwards, so a later test can't accidentally run against a nil installer.
func stubInstaller(t *testing.T, fn func(bin string, out io.Writer, args ...string) error) {
	t.Helper()
	prev := runInstaller
	runInstaller = fn
	t.Cleanup(func() { runInstaller = prev })
}

// pinUpdateAPI points the updater at a test server for one test.
func pinUpdateAPI(t *testing.T, base string) {
	t.Helper()
	prev := updateAPIBase
	updateAPIBase = base
	t.Cleanup(func() { updateAPIBase = prev })
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
		ok   bool
	}{
		{"1.7.0", "1.7.1", -1, true},
		{"1.7.1", "1.7.0", 1, true},
		{"1.7.0", "1.10.0", -1, true}, // numeric, not lexical: 10 > 7
		{"v1.7.0", "1.7.0", 0, true},
		{"1.8", "1.7.0", 1, true},
		{"1.7.0-beta.1", "1.7.0", -1, true}, // a pre-release precedes its release
		{"1.7.0", "1.7.0-beta.1", 1, true},
		{"1.7.0-beta.2", "1.7.0-beta.1", 1, true},
		{"1.7.0-beta.10", "1.7.0-beta.2", 1, true}, // numeric identifiers compare numerically
		{"1.7.0-alpha", "1.7.0-beta", -1, true},
		{"1.7.0+abc", "1.7.0+def", 0, true}, // build metadata takes no part in precedence
		{"dev+abc123-dirty", "1.7.0", 0, false},
		{"1.7.0", "dev", 0, false},
		{"", "1.7.0", 0, false},
	}
	for _, c := range cases {
		got, ok := CompareVersions(c.a, c.b)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("CompareVersions(%q, %q) = (%d, %v), want (%d, %v)", c.a, c.b, got, ok, c.want, c.ok)
		}
	}
}

// releaseServer serves a GitHub-shaped release plus its assets. handlerPath is
// the manifest path the test expects ("/releases/latest" or
// "/releases/tags/beta").
func releaseServer(t *testing.T, manifestPath, tag string, assets map[string][]byte) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == manifestPath {
			var b strings.Builder
			fmt.Fprintf(&b, `{"tag_name":%q,"assets":[`, tag)
			first := true
			for name := range assets {
				if !first {
					b.WriteString(",")
				}
				first = false
				fmt.Fprintf(&b, `{"name":%q,"browser_download_url":%q,"size":%d}`,
					name, srv.URL+"/dl/"+name, len(assets[name]))
			}
			b.WriteString(`]}`)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(b.String()))
			return
		}
		if body, ok := assets[strings.TrimPrefix(r.URL.Path, "/dl/")]; ok {
			_, _ = w.Write(body)
			return
		}
		http.Error(w, "not found: "+r.URL.Path, http.StatusNotFound)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// platformAsset is the archive name the updater looks for on THIS host, with the
// tag embedded the way a semver release publishes it.
func platformAsset(tag string) string {
	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("blamely_%s_%s_%s%s", tag, runtime.GOOS, runtime.GOARCH, ext)
}

// buildArchive packs `script` as the blamely binary under innerDir, matching the
// real release layout. Format follows the host so extraction exercises the same
// code path the updater will take here.
func buildArchive(t *testing.T, innerDir, script string) []byte {
	t.Helper()
	if runtime.GOOS == "windows" {
		return buildZip(t, innerDir+"/"+binaryName(), script)
	}
	return buildTarGz(t, innerDir+"/"+binaryName(), script)
}

// buildZip and buildTarGz build a named archive REGARDLESS of host, so the
// Windows-only zip path is covered when the suite runs on macOS/Linux (and the
// tar path when it runs on Windows). entry is the full in-archive path.
func buildZip(t *testing.T, entry, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(entry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func buildTarGz(t *testing.T, entry, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: entry, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeBinary is a shell script that answers `--version` like the real CLI, so it
// passes the sanity gate in step 7.
func fakeBinary(version string) string {
	return "#!/bin/sh\necho \"blamely version " + version + "\"\n"
}

// pinInstalledSelf makes the step-1 guard ("am I the installed copy?") pass, and
// returns ~/.blamely/bin.
//
// The guard compares executablePath() with InstalledBinaryPath() by file
// identity, so the test puts a placeholder at the installed path and points the
// seam at it. Deliberately NOT a hardlink of the running test binary: on Windows
// that leaves a locked image in the temp dir and t.TempDir() cleanup then fails
// with "Access is denied".
func pinInstalledSelf(t *testing.T) string {
	t.Helper()
	fakeHomeDir(t)
	dst, err := InstalledBinaryPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("placeholder installed binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	prev := executablePath
	executablePath = func() (string, error) { return dst, nil }
	t.Cleanup(func() { executablePath = prev })
	return filepath.Dir(dst)
}

func TestLatestRelease_StableUsesReleasesLatest(t *testing.T) {
	fakeHomeDir(t)
	archive := []byte("archive-bytes")
	assets := map[string][]byte{platformAsset("v1.8.0"): archive}
	srv := releaseServer(t, "/releases/latest", "v1.8.0", assets)

	rel, err := latestRelease(context.Background(), srv.URL, "latest")
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version != "1.8.0" || rel.Tag != "v1.8.0" {
		t.Fatalf("got version=%q tag=%q, want 1.8.0/v1.8.0", rel.Version, rel.Tag)
	}
	name, url, err := rel.ArchiveURL()
	if err != nil {
		t.Fatal(err)
	}
	if name != platformAsset("v1.8.0") || !strings.HasSuffix(url, name) {
		t.Fatalf("asset resolution: name=%q url=%q", name, url)
	}
}

func TestLatestRelease_BetaUsesBetaVersionAsset(t *testing.T) {
	fakeHomeDir(t)
	// The beta release's tag is the literal "beta" and its archives carry no
	// version, so the version has to come from the BETA_VERSION asset.
	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	unversioned := fmt.Sprintf("blamely_%s_%s%s", runtime.GOOS, runtime.GOARCH, ext)
	assets := map[string][]byte{
		"BETA_VERSION": []byte("1.8.0-beta.3\n"),
		unversioned:    []byte("archive-bytes"),
	}
	srv := releaseServer(t, "/releases/tags/beta", "beta", assets)

	rel, err := latestRelease(context.Background(), srv.URL, "beta")
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version != "1.8.0-beta.3" {
		t.Fatalf("version = %q, want 1.8.0-beta.3", rel.Version)
	}
	// The version-less asset name must still resolve.
	name, _, err := rel.ArchiveURL()
	if err != nil {
		t.Fatal(err)
	}
	if name != unversioned {
		t.Fatalf("asset = %q, want %q", name, unversioned)
	}
}

func TestUpdate_DownloadsVerifiesAndHandsOff(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the staged binary is a /bin/sh script; the sanity gate can't run it on Windows")
	}
	binDir := pinInstalledSelf(t)

	archive := buildArchive(t, "blamely_v1.8.0_"+runtime.GOOS+"_"+runtime.GOARCH, fakeBinary("1.8.0"))
	asset := platformAsset("v1.8.0")
	sums := fmt.Sprintf("%s  %s\n", sha256Hex(archive), asset)
	srv := releaseServer(t, "/releases/latest", "v1.8.0", map[string][]byte{
		asset:        archive,
		"SHA256SUMS": []byte(sums),
	})
	pinUpdateAPI(t, srv.URL)

	installed, err := InstalledBinaryPath()
	if err != nil {
		t.Fatal(err)
	}

	var gotBin string
	var gotArgs []string
	var installedAtHandOff string
	stubInstaller(t, func(bin string, _ io.Writer, args ...string) error {
		gotBin, gotArgs = bin, args
		// The whole point of placing the binary before the hand-off: by the time
		// the hand-off runs, the stable path ALREADY holds the new version.
		body, _ := os.ReadFile(installed)
		installedAtHandOff = string(body)
		return nil
	})

	res, err := Update(context.Background(), UpdateOptions{Current: "1.7.0", Out: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Updated || res.To != "1.8.0" {
		t.Fatalf("result = %+v, want Updated with To=1.8.0", res)
	}
	if !strings.Contains(installedAtHandOff, "1.8.0") {
		t.Errorf("stable path did not hold 1.8.0 when the hand-off ran:\n%s", installedAtHandOff)
	}
	// The hand-off runs the INSTALLED binary, not the staged copy: the staging
	// dir is gone by then, and running the installed one lets its CopyBinary
	// no-op instead of copying the file a second time.
	if gotBin != installed {
		t.Errorf("handed off %q, want the installed binary %q", gotBin, installed)
	}
	// The hand-off installs the IDE plugins too: CLI and plugins release in
	// lockstep at one version, so an update that moved only the CLI would leave the
	// editor plugin behind. --skip-plugins is the opt-out (asserted below).
	if strings.Join(gotArgs, " ") != "install" {
		t.Errorf("args = %v, want [install]", gotArgs)
	}
	// Staging is consumed once the binary is placed.
	if _, serr := os.Stat(filepath.Join(binDir, updateStagingPrefix+"v1.8.0")); !os.IsNotExist(serr) {
		t.Errorf("staging dir survived a successful update: %v", serr)
	}
}

// The hand-off's argv, asserted directly so it is covered on Windows too — the
// end-to-end test above is skipped there, and this is the whole of the
// plugins-follow-the-CLI decision.
func TestPostUpdateInstallArgs(t *testing.T) {
	if got := strings.Join(postUpdateInstallArgs(UpdateOptions{}), " "); got != "install" {
		t.Errorf("default = %q, want %q: an update must bring the IDE plugins along, "+
			"or the CLI drifts ahead of the editor plugin it releases in lockstep with", got, "install")
	}
	if got := strings.Join(postUpdateInstallArgs(UpdateOptions{SkipPlugins: true}), " "); got != "install --skip-plugins" {
		t.Errorf("SkipPlugins = %q, want %q", got, "install --skip-plugins")
	}
}

// TestUpdate_BinaryIsUpdatedEvenWhenHandOffFails is the reason step 8 places the
// binary itself. The hand-off is a child of this process and `blamely install`
// kills this process's tree on Windows, so it cannot be the step the update
// depends on. A failing hand-off must still leave the machine on the NEW
// version — hooks and the autostart entry all point at the stable path, which
// now holds it.
func TestUpdate_BinaryIsUpdatedEvenWhenHandOffFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("see TestUpdate_DownloadsVerifiesAndHandsOff")
	}
	pinInstalledSelf(t)

	archive := buildArchive(t, "blamely_v1.8.0_"+runtime.GOOS+"_"+runtime.GOARCH, fakeBinary("1.8.0"))
	asset := platformAsset("v1.8.0")
	sums := fmt.Sprintf("%s  %s\n", sha256Hex(archive), asset)
	srv := releaseServer(t, "/releases/latest", "v1.8.0", map[string][]byte{
		asset:        archive,
		"SHA256SUMS": []byte(sums),
	})
	pinUpdateAPI(t, srv.URL)

	stubInstaller(t, func(bin string, _ io.Writer, args ...string) error {
		return errors.New("fork/exec: the request is not supported")
	})

	var out bytes.Buffer
	res, err := Update(context.Background(), UpdateOptions{Current: "1.7.0", Out: &out})
	if err != nil {
		t.Fatalf("a failed hand-off must not fail the update: %v", err)
	}
	if !res.Updated || res.To != "1.8.0" {
		t.Fatalf("result = %+v, want Updated with To=1.8.0", res)
	}
	if res.Reason == "" {
		t.Error("a partial completion must be reported in Reason")
	}

	installed, err := InstalledBinaryPath()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(installed)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "1.8.0") {
		t.Errorf("the installed binary is not the new version:\n%s", body)
	}
	if !strings.Contains(out.String(), "`blamely install`") {
		t.Errorf("the user was not told how to finish the install:\n%s", out.String())
	}
}

func TestUpdate_ChecksumMismatchAborts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("see TestUpdate_DownloadsVerifiesAndHandsOff")
	}
	binDir := pinInstalledSelf(t)

	archive := buildArchive(t, "blamely_v1.8.0_"+runtime.GOOS+"_"+runtime.GOARCH, fakeBinary("1.8.0"))
	asset := platformAsset("v1.8.0")
	// A checksum for DIFFERENT bytes: exactly what a corrupted or swapped
	// download looks like.
	sums := fmt.Sprintf("%s  %s\n", sha256Hex([]byte("something else")), asset)
	srv := releaseServer(t, "/releases/latest", "v1.8.0", map[string][]byte{
		asset:        archive,
		"SHA256SUMS": []byte(sums),
	})
	pinUpdateAPI(t, srv.URL)

	called := false
	stubInstaller(t, func(bin string, _ io.Writer, args ...string) error { called = true; return nil })

	res, err := Update(context.Background(), UpdateOptions{Current: "1.7.0", Out: &bytes.Buffer{}})
	if err == nil {
		t.Fatal("expected an error on checksum mismatch")
	}
	if res.Updated || res.Reason != "checksum mismatch" {
		t.Errorf("result = %+v, want not-updated with reason 'checksum mismatch'", res)
	}
	if called {
		t.Error("installer ran despite a checksum mismatch")
	}
	// Staging must be cleaned up, not left behind holding a bad archive.
	left, _ := filepath.Glob(filepath.Join(binDir, updateStagingPrefix+"*"))
	if len(left) != 0 {
		t.Errorf("staging dirs left behind: %v", left)
	}
}

func TestUpdate_NoNewerVersionIsNoop(t *testing.T) {
	pinInstalledSelf(t)
	asset := platformAsset("v1.7.0")
	srv := releaseServer(t, "/releases/latest", "v1.7.0", map[string][]byte{asset: []byte("x")})
	pinUpdateAPI(t, srv.URL)

	called := false
	stubInstaller(t, func(bin string, _ io.Writer, args ...string) error { called = true; return nil })

	res, err := Update(context.Background(), UpdateOptions{Current: "1.7.0", Out: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated || res.Reason != "already up to date" {
		t.Fatalf("result = %+v, want not-updated with reason 'already up to date'", res)
	}
	if called {
		t.Error("installer ran with nothing to install")
	}
}

func TestUpdate_RefusesWhenNotInstalledCopy(t *testing.T) {
	// A fresh fake home with NO installed copy: os.Executable() (the test binary)
	// can't be it, which is exactly the local `go build` case. Version alone
	// cannot detect that — `version` is a hardcoded release string — so the guard
	// has to be path identity.
	fakeHomeDir(t)
	called := false
	stubInstaller(t, func(bin string, _ io.Writer, args ...string) error { called = true; return nil })

	res, err := Update(context.Background(), UpdateOptions{Current: "1.7.0", Out: &bytes.Buffer{}})
	if err == nil {
		t.Fatal("expected a refusal when running a non-installed copy")
	}
	if res.Reason != "not the installed binary" {
		t.Errorf("reason = %q, want 'not the installed binary'", res.Reason)
	}
	if called {
		t.Error("installer ran from a non-installed copy")
	}
}

func TestUpdate_FromArchiveRequiresChecksum(t *testing.T) {
	pinInstalledSelf(t)
	if _, err := Update(context.Background(), UpdateOptions{
		Current: "1.7.0", FromArchive: "/tmp/whatever.tar.gz", Out: &bytes.Buffer{},
	}); err == nil {
		t.Fatal("--from without --sha256 must be refused")
	}
}

func TestExtractBinary_FindsVersionedInnerDir(t *testing.T) {
	dir := t.TempDir()
	// Semver releases name the inner dir after the tag; the rolling beta does
	// not. Both must work, so the binary is found by walking entries.
	for _, inner := range []string{
		"blamely_v1.8.0_" + runtime.GOOS + "_" + runtime.GOARCH,
		"blamely_" + runtime.GOOS + "_" + runtime.GOARCH,
		"some-unexpected-dir",
	} {
		archive := filepath.Join(dir, "a.archive")
		name := archive + ".tar.gz"
		if runtime.GOOS == "windows" {
			name = archive + ".zip"
		}
		if err := os.WriteFile(name, buildArchive(t, inner, "binary-bytes"), 0o644); err != nil {
			t.Fatal(err)
		}
		dest := t.TempDir()
		got, err := extractBinary(name, dest)
		if err != nil {
			t.Fatalf("inner dir %q: %v", inner, err)
		}
		b, err := os.ReadFile(got)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != "binary-bytes" {
			t.Errorf("inner dir %q: extracted %q", inner, b)
		}
	}
}

// Both archive formats must extract on ANY host: the release pipeline ships
// .zip for Windows and .tar.gz everywhere else, and until this test the zip path
// was only ever exercised when the suite happened to run on Windows.
func TestExtractBinary_BothFormatsOnAnyHost(t *testing.T) {
	cases := []struct {
		name  string
		ext   string
		entry string
		build func(t *testing.T, entry, body string) []byte
	}{
		{"windows zip", ".zip", "blamely_v1.8.0_windows_amd64/blamely.exe", buildZip},
		{"unix tar.gz", ".tar.gz", "blamely_v1.8.0_linux_amd64/blamely", buildTarGz},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := filepath.Join(t.TempDir(), "release"+c.ext)
			if err := os.WriteFile(src, c.build(t, c.entry, "binary-bytes"), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := extractBinary(src, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			// The extracted file is named for the RUNNING host, not for the
			// archive it came from — that is what install.Run then places.
			if filepath.Base(got) != binaryName() {
				t.Errorf("extracted %q, want basename %q", got, binaryName())
			}
			b, err := os.ReadFile(got)
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != "binary-bytes" {
				t.Errorf("contents = %q", b)
			}
		})
	}
}

func TestExtractBinary_RejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("evil")
	if err := tw.WriteHeader(&tar.Header{
		Name: "../../evil", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()

	src := filepath.Join(dir, "evil.tar.gz")
	if err := os.WriteFile(src, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := extractBinary(src, t.TempDir()); err == nil {
		t.Fatal("path-traversal entry must be rejected")
	}
}

func TestParseChecksums(t *testing.T) {
	body := "" +
		"aa" + strings.Repeat("0", 62) + "  blamely_v1.8.0_darwin_arm64.tar.gz\n" +
		"bb" + strings.Repeat("0", 62) + "  ./blamely_v1.8.0_linux_amd64.tar.gz\n" +
		"garbage line\n"
	got := parseChecksums(body)
	if got["blamely_v1.8.0_darwin_arm64.tar.gz"] != "aa"+strings.Repeat("0", 62) {
		t.Errorf("plain name not parsed: %v", got)
	}
	if got["blamely_v1.8.0_linux_amd64.tar.gz"] != "bb"+strings.Repeat("0", 62) {
		t.Errorf("./-prefixed name not parsed: %v", got)
	}
	if len(got) != 2 {
		t.Errorf("garbage line not skipped: %v", got)
	}
}

func TestCleanStaleUpdateStaging(t *testing.T) {
	dir := t.TempDir()
	fresh := filepath.Join(dir, updateStagingPrefix+"v1.8.0")
	stale := filepath.Join(dir, updateStagingPrefix+"v1.7.0")
	for _, d := range []string{fresh, stale} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * updateStagingMaxAge)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	cleanStaleUpdateStaging(dir)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("stale staging dir was not reaped")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("fresh staging dir must be left alone (another run may be using it)")
	}
}
