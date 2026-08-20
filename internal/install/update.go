package install

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/blamely/blamely/internal/config"

	"github.com/blamely/blamely/internal/procattr"
)

// Version is the running CLI version, assigned once at startup by cmd/blamely
// exactly like gitnotes.Version and report.Version. It lives here because
// resolveVersion() is in package main, which the daemon-side update checker
// cannot reach.
var Version = "dev"

// updateAPIBase is the releases API root the updater talks to. A var (not const)
// so tests can point it at an httptest server, mirroring releaseAPIBase. At
// runtime $BLAMELY_UPDATE_API or update.api_base take precedence — see updateAPI.
var updateAPIBase = releaseAPIBase

const (
	updateManifestTimeout = 5 * time.Second
	updateDownloadTimeout = 60 * time.Second
	updateSanityTimeout   = 10 * time.Second

	// maxUpdateArchiveBytes caps a download so a wrong URL (a proxy's HTML error
	// page, a redirect loop) can't fill the disk.
	maxUpdateArchiveBytes = 200 << 20

	// updateStagingPrefix names the staging dirs under ~/.blamely/bin. Staging
	// there — not in os.TempDir() — is deliberate: the extracted binary is moved
	// into place with os.Rename, which fails with EXDEV across filesystems (on
	// many Linux distros /tmp is tmpfs).
	updateStagingPrefix = ".update-"
	updateStagingMaxAge = 24 * time.Hour
)

// releaseAsset is one downloadable file attached to a GitHub release.
type releaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// Release is a resolved, installable release: its version, the tag it came from,
// and its assets by name.
type Release struct {
	Version string            // normalized, no leading "v"
	Tag     string            // "v1.7.0", or "beta" for the rolling pre-release
	Assets  map[string]string // asset name -> download URL
}

// UpdateOptions configures one Update run.
type UpdateOptions struct {
	Current     string // running version; defaults to Version
	Channel     string // defaults to UpdateChannel()
	Force       bool   // install even at the same version, and from a non-installed copy
	DryRun      bool   // resolve and compare, then stop before downloading
	WithPlugins bool   // also reinstall the IDE plugins (off: never clobber a sideloaded dev build)
	FromArchive string // air-gap: install this local archive instead of downloading
	ExpectSHA   string // required with FromArchive — the archive's sha256
	Out         io.Writer
}

// UpdateResult reports what an Update run did. Reason explains a no-op.
type UpdateResult struct {
	From    string
	To      string
	Updated bool
	Reason  string
}

// runInstaller executes the staged binary's own `install`, sending its output to
// out. A package var so tests can assert the hand-off without running a real
// install (the same seam pattern as tools.cursorHomeDir).
//
// It deliberately does NOT inherit this process's own standard handles, which is
// what it used to do:
//
//   - stdin is left nil (the child gets NUL / /dev/null). `blamely install` has
//     prompts, but none that a hand-off should answer: the JetBrains
//     plugin-locked retry is skipped when stdin is not interactive, and the
//     uninstall blocker prompt treats a read error as "no". Inheriting ours
//     bought nothing and could only fail.
//   - stdout/stderr follow out, and an out that is an unusable *os.File is
//     dropped rather than passed on. A console-less caller — the daemon's
//     auto-update, or `blamely update` from a Scheduled Task — otherwise hands
//     CreateProcess a std handle it rejects with ERROR_NOT_SUPPORTED, failing
//     the hand-off for a staged binary that had just run fine. See
//     usableStdHandle in stdio_windows.go for the mechanism.
var runInstaller = func(bin string, out io.Writer, args ...string) error {
	cmd := procattr.Hide(exec.Command(bin, args...))
	// One writer value for both streams, deliberately: os/exec gives the child a
	// single pipe when Stdout and Stderr are interface-equal, so the two streams
	// interleave into one goroutine's writes. Assigning two separately-derived
	// values would make os/exec copy them concurrently and race any writer that
	// is not safe for concurrent use.
	w := childOutput(out)
	cmd.Stdout, cmd.Stderr = w, w
	return cmd.Run()
}

// childOutput adapts out for use as a child process's stdout/stderr. os/exec
// inherits an *os.File's handle directly and pipes anything else, so this is
// also the seam that decides which of those two the child gets.
func childOutput(out io.Writer) io.Writer {
	if out == nil {
		return io.Discard
	}
	if f, ok := out.(*os.File); ok && !usableStdHandle(f) {
		return io.Discard
	}
	return out
}

// executablePath resolves this process's own binary for the "am I the installed
// copy?" guard. A package var so a test can point it at an ordinary file:
// hardlinking the running test binary into a temp dir would work on Unix but
// leaves a LOCKED image on Windows, which then fails t.TempDir() cleanup with
// "Access is denied".
var executablePath = os.Executable

// UpdateChannel resolves which release channel to update from: $BLAMELY_CHANNEL
// (one-off override, mirrors the installer), then update.channel in config (how
// a fleet pins itself), then "latest".
func UpdateChannel() string {
	if c := strings.TrimSpace(os.Getenv("BLAMELY_CHANNEL")); c != "" {
		return c
	}
	if c := strings.TrimSpace(config.LoadConfig().Update.Channel); c != "" {
		return c
	}
	return "latest"
}

// updateAPI resolves the releases API root: $BLAMELY_UPDATE_API, then
// update.api_base in config, then the public repo. The overrides exist so a corp
// network that blocks api.github.com can mirror the releases internally.
func updateAPI() string {
	if v := strings.TrimSpace(os.Getenv("BLAMELY_UPDATE_API")); v != "" {
		return strings.TrimRight(v, "/")
	}
	if v := strings.TrimSpace(config.LoadConfig().Update.APIBase); v != "" {
		return strings.TrimRight(v, "/")
	}
	return updateAPIBase
}

// UpdateCheckDisabled reports whether the periodic check is switched off, by env
// kill switch (mirroring BLAMELY_NO_RELEASE_NOTES) or by config.
func UpdateCheckDisabled() bool {
	if v := strings.TrimSpace(os.Getenv("BLAMELY_NO_UPDATE_CHECK")); v != "" && v != "0" {
		return true
	}
	return !config.LoadConfig().Update.Check
}

// LatestRelease resolves the newest release on a channel.
func LatestRelease(ctx context.Context, channel string) (Release, error) {
	return latestRelease(ctx, updateAPI(), channel)
}

func latestRelease(ctx context.Context, base, channel string) (Release, error) {
	channel = strings.TrimSpace(channel)
	if channel == "" {
		channel = "latest"
	}
	// The stable channel resolves through /releases/latest so we get the real
	// semver tag (v1.8.0) and its VERSIONED asset names. /releases/tags/latest is
	// the ROLLING copy of the same build: its tag is the literal string "latest",
	// which carries no version at all.
	url := base + "/releases/tags/" + channel
	if channel == "latest" {
		url = base + "/releases/latest"
	}
	info, err := fetchRelease(ctx, url)
	if err != nil {
		return Release{}, err
	}
	rel := Release{Tag: info.TagName, Assets: map[string]string{}}
	for _, a := range info.Assets {
		if a.Name != "" && a.URL != "" {
			rel.Assets[a.Name] = a.URL
		}
	}
	rel.Version = normalizeVersion(info.TagName)
	// A rolling release's tag is not a version ("beta"), so the pipeline
	// publishes the version as a BETA_VERSION asset alongside the archives.
	if _, ok := parseSemver(rel.Version); !ok {
		if v := fetchVersionAsset(ctx, rel.Assets); v != "" {
			rel.Version = normalizeVersion(v)
		}
	}
	if rel.Version == "" {
		return rel, fmt.Errorf("release %q: no version in tag or version asset", channel)
	}
	return rel, nil
}

func fetchRelease(ctx context.Context, url string) (releaseInfo, error) {
	var info releaseInfo
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return info, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return info, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return info, fmt.Errorf("releases api %s: status %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return info, fmt.Errorf("decode release: %w", err)
	}
	return info, nil
}

// fetchVersionAsset reads the release's version-carrying text asset (BETA_VERSION
// on the beta channel). Best-effort: "" when absent or unreadable.
func fetchVersionAsset(ctx context.Context, assets map[string]string) string {
	for _, name := range []string{"BETA_VERSION", "VERSION"} {
		url, ok := assets[name]
		if !ok {
			continue
		}
		body, err := fetchBytes(ctx, url, 1<<10)
		if err != nil {
			continue
		}
		if v := strings.TrimSpace(string(body)); v != "" {
			return v
		}
	}
	return ""
}

func fetchBytes(ctx context.Context, url string, max int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, max))
}

// CheckForUpdate reports whether the channel's newest release is newer than
// current. An unparseable current version (a local `go build` stamp) yields
// newer=false and no error: we cannot tell, so we must not nag or auto-apply.
func CheckForUpdate(ctx context.Context, current string) (rel Release, newer bool, err error) {
	rel, err = LatestRelease(ctx, UpdateChannel())
	if err != nil {
		return Release{}, false, err
	}
	n, ok := CompareVersions(rel.Version, current)
	if !ok {
		return rel, false, nil
	}
	return rel, n > 0, nil
}

// Update downloads, verifies and installs a newer blamely. See the numbered
// steps below; nothing on disk is touched until the final hand-off, so a failure
// at any earlier point leaves the running install exactly as it was.
func Update(ctx context.Context, opts UpdateOptions) (UpdateResult, error) {
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	if strings.TrimSpace(opts.Current) == "" {
		opts.Current = Version
	}
	res := UpdateResult{From: opts.Current, To: opts.Current}

	dst, err := InstalledBinaryPath()
	if err != nil {
		return res, err
	}
	binDir := filepath.Dir(dst)

	// 1. Only the installed copy may replace the installed copy. Version alone
	// cannot detect a dev build — `version` is a hardcoded string that a local
	// `go build` inherits — but path identity can.
	if !opts.Force {
		self, err := executablePath()
		if err != nil {
			return res, fmt.Errorf("locate blamely binary: %w", err)
		}
		if same, _ := sameFile(self, dst); !same {
			res.Reason = "not the installed binary"
			return res, fmt.Errorf("this is not the installed copy at %s — run `blamely install` first, or pass --force", dst)
		}
	}

	// Reap staging dirs abandoned by an earlier interrupted run.
	cleanStaleUpdateStaging(binDir)

	var (
		tag        string
		wantSHA    string
		wantVer    string
		archiveSrc string // local path, or "" to download
		archiveURL string
		assetName  string
	)

	if opts.FromArchive != "" {
		// Air-gap path: an archive the admin already has. The checksum is
		// REQUIRED — without it there is nothing to verify against, and a
		// silently-corrupt archive would be installed as if it were genuine.
		if strings.TrimSpace(opts.ExpectSHA) == "" {
			return res, fmt.Errorf("--from requires --sha256 (the archive's sha256 checksum)")
		}
		archiveSrc = opts.FromArchive
		wantSHA = strings.ToLower(strings.TrimSpace(opts.ExpectSHA))
		tag = "local"
	} else {
		channel := opts.Channel
		if strings.TrimSpace(channel) == "" {
			channel = UpdateChannel()
		}
		mctx, cancel := context.WithTimeout(ctx, updateManifestTimeout)
		rel, err := LatestRelease(mctx, channel)
		cancel()
		if err != nil {
			return res, err
		}
		res.To = rel.Version
		tag, wantVer = rel.Tag, rel.Version

		// 2. Compare. An unknown current version is refused rather than guessed.
		n, ok := CompareVersions(rel.Version, opts.Current)
		if !ok && !opts.Force {
			res.Reason = "current version is not a release build"
			return res, fmt.Errorf("running version %q is not a release version — pass --force to install %s anyway", opts.Current, rel.Version)
		}
		if ok && n <= 0 && !opts.Force {
			res.Reason = "already up to date"
			fmt.Fprintf(out, "blamely %s is up to date (channel %s)\n", opts.Current, channel)
			return res, nil
		}
		assetName, archiveURL, err = rel.ArchiveURL()
		if err != nil {
			return res, err
		}
		if opts.DryRun {
			res.Reason = "dry run"
			fmt.Fprintf(out, "would update %s -> %s (%s)\n", opts.Current, rel.Version, assetName)
			return res, nil
		}
		fmt.Fprintf(out, "updating %s -> %s (%s)\n", opts.Current, rel.Version, assetName)
	}

	// 3. Stage on the SAME filesystem as the destination (see updateStagingPrefix).
	stage := filepath.Join(binDir, updateStagingPrefix+sanitizeTag(tag))
	_ = os.RemoveAll(stage)
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return res, fmt.Errorf("create staging dir: %w", err)
	}
	staged := false
	defer func() {
		if !staged {
			_ = os.RemoveAll(stage)
		}
	}()

	archivePath := archiveSrc
	if archivePath == "" {
		dctx, cancel := context.WithTimeout(ctx, updateDownloadTimeout)
		archivePath = filepath.Join(stage, assetName)
		err := downloadTo(dctx, archiveURL, archivePath)
		cancel()
		if err != nil {
			return res, fmt.Errorf("download %s: %w", assetName, err)
		}
		// 4. Verify against SHA256SUMS from the SAME release. This is an
		// INTEGRITY check, not an authenticity one: SHA256SUMS.asc is GPG-signed
		// but no public key is embedded here yet, so this does not defend
		// against a compromised release account.
		sctx, scancel := context.WithTimeout(ctx, updateManifestTimeout)
		wantSHA, err = releaseChecksum(sctx, archiveURL, assetName)
		scancel()
		if err != nil {
			return res, err
		}
	}

	if err := verifySHA256(archivePath, wantSHA); err != nil {
		res.Reason = "checksum mismatch"
		return res, err
	}

	// 5. Extract. The archive's inner directory is named after the release
	// (blamely_v1.8.0_darwin_arm64/) on semver builds but NOT on the rolling
	// beta, so the binary is found by walking entries, never by a built path.
	binPath, err := extractBinary(archivePath, stage)
	if err != nil {
		return res, err
	}

	// 6. De-quarantine + ad-hoc re-sign BEFORE running it: on Apple Silicon an
	// unsigned Mach-O is killed with SIGKILL, which would fail step 7 for a
	// perfectly good binary. No-op on Linux/Windows.
	_ = prepareInstalledBinary(binPath)

	// 7. Sanity gate — the rollback insurance. A truncated, wrong-arch or
	// Gatekeeper-blocked binary is caught HERE, before anything is replaced.
	gotVer, err := binaryVersion(ctx, binPath)
	if err != nil {
		res.Reason = "staged binary failed to run"
		return res, fmt.Errorf("staged binary did not run: %w", err)
	}
	if wantVer != "" && !strings.Contains(gotVer, wantVer) {
		res.Reason = "version mismatch"
		return res, fmt.Errorf("staged binary reports %q, expected %s", gotVer, wantVer)
	}
	if wantVer == "" {
		res.To = normalizeVersion(lastField(gotVer))
	}

	// 8. Put the new binary at the stable path OURSELVES, by rename.
	//
	// Everything that invokes blamely — each tool's PostToolUse hook, the global
	// git hook, the autostart entry and the Windows keepalive task — spells that
	// path literally and none of them is rewritten by an update. So swapping the
	// file behind it IS the update: nothing else has to succeed for the new
	// version to take over the next time the daemon starts.
	//
	// placeBinary makes that safe on Windows, where a running image cannot be
	// overwritten but CAN be renamed: it moves the locked destination aside as
	// <name>.old-<ts>, puts the new one in place, and rolls back if that fails.
	// The old process keeps running from the renamed file until it restarts.
	//
	// This runs here rather than inside the hand-off below because the hand-off
	// is a CHILD of this process, and `blamely install` kills this process —
	// with its tree — on Windows. Anything that must survive for the update to
	// count cannot depend on that child finishing.
	installedPath, err := CopyBinary(binPath)
	if err != nil {
		res.Reason = "could not replace the installed binary"
		return res, fmt.Errorf("install %s failed (the previous version is still in place): %w", res.To, err)
	}
	staged = true
	_ = os.RemoveAll(stage)
	res.Updated = true

	// 9. Hand the REST off to the now-installed binary: re-register every tool
	// hook (a new release may add one) and restart the daemon agent so the new
	// version takes over immediately instead of at the next start.
	//
	// Best-effort by design. Step 8 already updated the version, so a failure
	// here costs a hook refresh and an immediate restart — not the update. On
	// Windows this step routinely takes the daemon (and, through the same kill,
	// itself) down; the keepalive task then revives the daemon from the stable
	// path, which now holds the new binary.
	args := []string{"install"}
	if !opts.WithPlugins {
		args = append(args, "--skip-plugins")
	}
	if err := runInstaller(installedPath, out, args...); err != nil {
		res.Reason = "installed; the post-install step did not finish"
		fmt.Fprintf(out, "blamely %s is in place, but finishing the install failed: %v\n"+
			"Hooks keep working through the unchanged path. Run `blamely install` to refresh them.\n",
			res.To, err)
	}
	return res, nil
}

// ArchiveURL picks this platform's archive from the release. Semver releases put
// the tag in the asset name; the rolling latest/beta releases publish the same
// archives without it, so both spellings are tried.
func (r Release) ArchiveURL() (name, url string, err error) {
	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	suffix := fmt.Sprintf("_%s_%s%s", runtime.GOOS, runtime.GOARCH, ext)
	for _, n := range []string{"blamely_" + r.Tag + suffix, "blamely" + suffix} {
		if u, ok := r.Assets[n]; ok {
			return n, u, nil
		}
	}
	return "", "", fmt.Errorf("release %s has no asset for %s/%s", r.Tag, runtime.GOOS, runtime.GOARCH)
}

// releaseChecksum fetches SHA256SUMS from the same release directory as the
// archive and returns the expected sum for assetName.
func releaseChecksum(ctx context.Context, archiveURL, assetName string) (string, error) {
	i := strings.LastIndex(archiveURL, "/")
	if i < 0 {
		return "", fmt.Errorf("cannot locate SHA256SUMS for %s", assetName)
	}
	body, err := fetchBytes(ctx, archiveURL[:i+1]+"SHA256SUMS", 1<<20)
	if err != nil {
		return "", fmt.Errorf("fetch SHA256SUMS: %w", err)
	}
	sum := parseChecksums(string(body))[assetName]
	if sum == "" {
		return "", fmt.Errorf("SHA256SUMS has no entry for %s", assetName)
	}
	return sum, nil
}

// parseChecksums reads `<sha256>  <name>` lines, tolerating the `./name` form
// sha256sum emits when hashing a directory.
func parseChecksums(body string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(body, "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) != 2 || len(f[0]) != 64 {
			continue
		}
		out[strings.TrimPrefix(strings.TrimPrefix(f[1], "*"), "./")] = strings.ToLower(f[0])
	}
	return out
}

func verifySHA256(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", filepath.Base(path), got, want)
	}
	return nil
}

func downloadTo(ctx context.Context, url, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxUpdateArchiveBytes+1))
	if err != nil {
		return err
	}
	if n > maxUpdateArchiveBytes {
		return fmt.Errorf("archive exceeds %d bytes", int64(maxUpdateArchiveBytes))
	}
	return f.Close()
}

// extractBinary pulls the blamely executable out of a release archive into
// destDir and returns its path.
func extractBinary(archivePath, destDir string) (string, error) {
	dst := filepath.Join(destDir, binaryName())
	var err error
	if strings.HasSuffix(strings.ToLower(archivePath), ".zip") {
		err = extractBinaryZip(archivePath, dst)
	} else {
		err = extractBinaryTarGz(archivePath, dst)
	}
	if err != nil {
		return "", err
	}
	if err := os.Chmod(dst, 0o755); err != nil {
		return "", err
	}
	return dst, nil
}

func binaryName() string {
	if runtime.GOOS == "windows" {
		return "blamely.exe"
	}
	return "blamely"
}

// isBinaryEntry reports whether an archive entry is the blamely executable.
// Matching on the BASE name is what makes this channel-agnostic: the enclosing
// directory is blamely_v1.8.0_darwin_arm64/ on a semver release and
// blamely_windows_amd64/ on the rolling beta.
func isBinaryEntry(name string) bool {
	base := filepath.Base(filepath.FromSlash(name))
	return base == "blamely" || base == "blamely.exe"
}

// safeArchivePath rejects entries that would escape the destination directory
// ("../../evil", absolute paths) — a malicious or malformed archive must not be
// able to write outside the staging dir.
func safeArchivePath(name string) error {
	clean := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		return fmt.Errorf("unsafe archive entry %q", name)
	}
	return nil
}

func extractBinaryTarGz(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gunzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		if err := safeArchivePath(h.Name); err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg || !isBinaryEntry(h.Name) {
			continue
		}
		return writeFileFrom(io.LimitReader(tr, maxUpdateArchiveBytes), dst)
	}
	return fmt.Errorf("archive %s contains no blamely binary", filepath.Base(src))
}

func extractBinaryZip(src, dst string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer zr.Close()
	for _, e := range zr.File {
		if err := safeArchivePath(e.Name); err != nil {
			return err
		}
		if e.FileInfo().IsDir() || !isBinaryEntry(e.Name) {
			continue
		}
		rc, err := e.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		return writeFileFrom(io.LimitReader(rc, maxUpdateArchiveBytes), dst)
	}
	return fmt.Errorf("archive %s contains no blamely binary", filepath.Base(src))
}

func writeFileFrom(r io.Reader, dst string) error {
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// binaryVersion runs `<bin> --version` and returns its output. This is the gate
// that proves the staged binary actually executes on this machine.
func binaryVersion(ctx context.Context, bin string) (string, error) {
	vctx, cancel := context.WithTimeout(ctx, updateSanityTimeout)
	defer cancel()
	out, err := procattr.Hide(exec.CommandContext(vctx, bin, "--version")).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// cleanStaleUpdateStaging removes staging dirs left behind by an interrupted
// run, mirroring cleanStaleBinaryBackups. Best-effort.
func cleanStaleUpdateStaging(binDir string) {
	matches, _ := filepath.Glob(filepath.Join(binDir, updateStagingPrefix+"*"))
	for _, m := range matches {
		info, err := os.Stat(m)
		if err != nil || time.Since(info.ModTime()) < updateStagingMaxAge {
			continue
		}
		_ = os.RemoveAll(m)
	}
}

// sanitizeTag makes a release tag safe as a directory name component.
func sanitizeTag(tag string) string {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return "unknown"
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		}
		return '-'
	}, tag)
}

func normalizeVersion(s string) string {
	return strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(s), "v"), "V")
}

// lastField returns the last whitespace-separated token, so the cobra
// `blamely version 1.8.0` line yields the version.
func lastField(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return f[len(f)-1]
}

// semver is the ordered part of a version: the numeric core plus an optional
// pre-release tag. Build metadata (+abc123) is deliberately dropped — semver
// says it takes no part in precedence.
type semver struct {
	nums [3]int
	pre  string
}

func parseSemver(s string) (semver, bool) {
	var v semver
	s = normalizeVersion(s)
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	core := s
	if i := strings.IndexByte(s, '-'); i >= 0 {
		core, v.pre = s[:i], s[i+1:]
	}
	parts := strings.Split(core, ".")
	if core == "" || len(parts) > 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v.nums[i] = n
	}
	return v, true
}

// CompareVersions returns -1, 0 or +1 for a vs b. ok=false when either side is
// not a release version (a `dev+abc123-dirty` build stamp) — callers MUST treat
// that as "unknown": never nag, never auto-apply.
func CompareVersions(a, b string) (n int, ok bool) {
	va, oka := parseSemver(a)
	vb, okb := parseSemver(b)
	if !oka || !okb {
		return 0, false
	}
	for i := 0; i < 3; i++ {
		if va.nums[i] != vb.nums[i] {
			if va.nums[i] < vb.nums[i] {
				return -1, true
			}
			return 1, true
		}
	}
	return comparePrerelease(va.pre, vb.pre), true
}

// comparePrerelease implements semver precedence for pre-release tags: a version
// WITH one sorts before the same version without (1.7.0-beta.1 < 1.7.0), and tags
// compare identifier by identifier with numeric ones ordering before alphanumeric.
func comparePrerelease(a, b string) int {
	if a == b {
		return 0
	}
	if a == "" {
		return 1
	}
	if b == "" {
		return -1
	}
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		if as[i] == bs[i] {
			continue
		}
		an, aerr := strconv.Atoi(as[i])
		bn, berr := strconv.Atoi(bs[i])
		switch {
		case aerr == nil && berr == nil:
			if an < bn {
				return -1
			}
			return 1
		case aerr == nil:
			return -1
		case berr == nil:
			return 1
		default:
			if as[i] < bs[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(as) < len(bs):
		return -1
	case len(as) > len(bs):
		return 1
	}
	return 0
}
