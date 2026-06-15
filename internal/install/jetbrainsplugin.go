package install

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/blamely/blamely/internal/config"
)

// blamelyJetBrainsPluginID is the numeric JetBrains Marketplace id for Blamely
// (xmlId "ai.blamely"): https://plugins.jetbrains.com/plugin/31746-blamely
const blamelyJetBrainsPluginID = 31746

// blamelyJetBrainsDirGlob matches the plugin's installed directory name across
// versions. JetBrains plugin distribution zips contain a single top-level
// directory named after the Gradle project ("Blamely-intellij[-suffix]"),
// which lands directly under <configDir>/plugins/ once unzipped — this glob
// is how we recognise "already installed" and what `uninstall` removes.
const blamelyJetBrainsDirGlob = "Blamely-intellij*"

// jetbrainsIDELabels is the set of JetBrains product names we offer the
// Blamely plugin to, matched against each detected IDE's product-info.json
// "name" field. Mirrors the plugin's listed compatibleVersions
// (IDEA/WEBSTORM/DATASPELL/PYCHARM/…either Ultimate or Community editions).
var jetbrainsIDELabels = map[string]bool{
	"IntelliJ IDEA":           true,
	"IntelliJ IDEA Community": true,
	"WebStorm":                true,
	"DataGrip":                true,
	"PyCharm":                 true,
	"PyCharm Community":       true,
	"PhpStorm":                true,
	"GoLand":                  true,
	"CLion":                   true,
	"RubyMine":                true,
	"Rider":                   true,
	"DataSpell":               true,
}

// jetbrainsIDE is one detected JetBrains IDE installation: enough to compute
// its plugin directory and ask the marketplace for a build-compatible update.
type jetbrainsIDE struct {
	Label       string // display name, e.g. "IntelliJ IDEA"
	BuildNumber string // e.g. "IU-261.22158.277" — the marketplace compatibility query key
	PluginsDir  string // ~/Library/Application Support/JetBrains/<dataDir>/plugins
}

// productInfo is the subset of <App>.app/Contents/Resources/product-info.json
// we need. It's the same file the IDE itself reads to find its per-version
// config/plugins directory and build number — reading it directly means we
// don't have to guess from directory-naming conventions that shift release to
// release (IntelliJIdea2026.1 vs IU2026.1, seen side-by-side on this machine).
type productInfo struct {
	Name              string `json:"name"`
	ProductCode       string `json:"productCode"`
	DataDirectoryName string `json:"dataDirectoryName"`
	BuildNumber       string `json:"buildNumber"`
}

// JetBrainsPluginResult is one row of the install log's "Editors" group: the
// outcome of trying to get the Blamely plugin into a single JetBrains IDE.
type JetBrainsPluginResult struct {
	Label      string
	PluginsDir string
	Installed  bool // true when THIS run did the initial install
	Updated    bool // true when the plugin was already present and we re-extracted the latest build
	Err        error
}

// InstallJetBrainsPlugins drives the marketplace install for every detected
// JetBrains IDE: download a build-compatible plugin zip from the JetBrains
// Marketplace API and unzip it into the IDE's plugins directory — the same
// artifact "Install Plugin from Disk" consumes, just fetched headlessly.
//
// This is the fallback path, not a CLI install: JetBrains IDEs are
// single-instance, so a headless `idea installPlugins <id>` would just be
// silently forwarded to an already-running instance and do nothing useful —
// the marketplace download is the only mechanism that works regardless of
// whether the IDE is currently open.
func InstallJetBrainsPlugins() []JetBrainsPluginResult {
	ides, err := findJetBrainsIDEs()
	if err != nil || len(ides) == 0 {
		return nil
	}

	// The plugin zip is identical for every IDE whose build falls in the same
	// compatibility window (true for the 2025.x/2026.x line today), so we
	// cache the last-downloaded file and only re-fetch when the marketplace
	// hands back a different build for a given IDE.
	var cachedFile, cachedZipPath string
	defer func() {
		if cachedZipPath != "" {
			_ = os.Remove(cachedZipPath)
		}
	}()

	results := make([]JetBrainsPluginResult, 0, len(ides))
	for _, ide := range ides {
		if !jetbrainsIDELabels[ide.Label] {
			continue
		}
		r := JetBrainsPluginResult{Label: ide.Label, PluginsDir: ide.PluginsDir}
		// Reinstall on every run (don't skip when already present): this both
		// installs the plugin the first time and refreshes it to the latest
		// build-compatible version, matching the VS Code-family `--force` path
		// so a stale plugin never lingers. Only reached when installPlugins=true
		// (end-user installers), so a sideloaded dev build is never clobbered.
		alreadyPresent := hasJetBrainsPlugin(ide.PluginsDir)

		file, ferr := compatiblePluginFile(ide.BuildNumber)
		if ferr != nil {
			r.Err = ferr
			results = append(results, r)
			continue
		}
		if cachedZipPath == "" || cachedFile != file {
			if cachedZipPath != "" {
				_ = os.Remove(cachedZipPath)
				cachedZipPath = ""
			}
			zipPath, derr := downloadPluginZip(file)
			if derr != nil {
				r.Err = derr
				results = append(results, r)
				continue
			}
			cachedFile, cachedZipPath = file, zipPath
		}
		// Remove the existing plugin dir(s) before extracting so a renamed or
		// older-versioned directory can't sit alongside the fresh one.
		if alreadyPresent {
			_ = removeJetBrainsPlugin(ide.PluginsDir)
		}
		if eerr := extractPluginZip(cachedZipPath, ide.PluginsDir); eerr != nil {
			r.Err = eerr
		} else if alreadyPresent {
			r.Updated = true
		} else {
			r.Installed = true
		}
		results = append(results, r)
	}
	return results
}

// UninstallJetBrainsPlugins removes the Blamely plugin directory from every
// plugins directory in `pluginsDirs` — the set this tool installed into.
// Mirrors UninstallEditorExtensions: only ever removes what WE installed,
// never a plugin the user (or an IDE's own marketplace browser) added.
func UninstallJetBrainsPlugins(pluginsDirs []string) error {
	var firstErr error
	for _, dir := range pluginsDirs {
		if err := removeJetBrainsPlugin(dir); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// removeJetBrainsPlugin deletes every Blamely plugin directory under one IDE's
// plugins dir (there should be at most one, but a botched prior install could
// leave a renamed duplicate). Returns the first removal error, if any.
func removeJetBrainsPlugin(pluginsDir string) error {
	matches, _ := filepath.Glob(filepath.Join(pluginsDir, blamelyJetBrainsDirGlob))
	var firstErr error
	for _, m := range matches {
		if err := os.RemoveAll(m); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func hasJetBrainsPlugin(pluginsDir string) bool {
	matches, _ := filepath.Glob(filepath.Join(pluginsDir, blamelyJetBrainsDirGlob))
	return len(matches) > 0
}

// findJetBrainsIDEs scans the usual macOS app locations for JetBrains IDEs by
// reading their bundled product-info.json. Toolbox-managed and manually
// installed IDEs both end up under /Applications or ~/Applications as plain
// .app bundles (confirmed: IntelliJ IDEA + WebStorm under ~/Applications,
// DataGrip under /Applications — all Toolbox-managed on this machine).
func findJetBrainsIDEs() ([]jetbrainsIDE, error) {
	home, err := config.Home()
	if err != nil {
		return nil, err
	}

	// Per OS: every product-info.json to inspect (one per installed IDE) plus the
	// base dir holding each IDE's per-user config (where its plugins/ dir lives).
	infoPaths, configBase := jetbrainsInfoPathsAndConfigBase(home)
	if configBase == "" {
		return nil, nil // unsupported OS
	}

	var ides []jetbrainsIDE
	seen := map[string]bool{}
	for _, infoPath := range infoPaths {
		data, err := os.ReadFile(infoPath)
		if err != nil {
			continue
		}
		var pi productInfo
		if err := json.Unmarshal(data, &pi); err != nil ||
			pi.DataDirectoryName == "" || pi.BuildNumber == "" || pi.ProductCode == "" {
			continue
		}
		configDir := filepath.Join(configBase, pi.DataDirectoryName)
		if !dirExists(configDir) || seen[configDir] {
			continue // not a real per-user JetBrains config layout, or a duplicate install
		}
		seen[configDir] = true
		ides = append(ides, jetbrainsIDE{
			Label:       pi.Name,
			BuildNumber: pi.ProductCode + "-" + pi.BuildNumber,
			PluginsDir:  filepath.Join(configDir, "plugins"),
		})
	}
	return ides, nil
}

// jetbrainsInfoPathsAndConfigBase returns the product-info.json paths to inspect
// and the per-user JetBrains config base for the host OS. Every JetBrains IDE
// ships a product-info.json in its install root (under Contents/Resources on
// macOS) and keeps per-user config — including plugins/ — under a versioned
// subdir of the config base. Returns configBase=="" on an unsupported OS.
func jetbrainsInfoPathsAndConfigBase(home string) (infoPaths []string, configBase string) {
	switch runtime.GOOS {
	case "darwin":
		for _, base := range []string{"/Applications", filepath.Join(home, "Applications")} {
			matches, _ := filepath.Glob(filepath.Join(base, "*.app"))
			for _, app := range matches {
				infoPaths = append(infoPaths, filepath.Join(app, "Contents", "Resources", "product-info.json"))
			}
		}
		return infoPaths, filepath.Join(home, "Library", "Application Support", "JetBrains")
	case "windows":
		var roots []string
		if p := os.Getenv("LOCALAPPDATA"); p != "" {
			roots = append(roots,
				filepath.Join(p, "Programs"),                     // Toolbox default install root
				filepath.Join(p, "JetBrains", "Toolbox", "apps"), // Toolbox app store
			)
		}
		for _, pf := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")} {
			if pf != "" {
				roots = append(roots, filepath.Join(pf, "JetBrains"))
			}
		}
		for _, root := range roots {
			infoPaths = append(infoPaths, findFilesUpTo(root, "product-info.json", 4)...)
		}
		appdata := os.Getenv("APPDATA")
		if appdata == "" {
			appdata = filepath.Join(home, "AppData", "Roaming")
		}
		return infoPaths, filepath.Join(appdata, "JetBrains")
	case "linux":
		roots := []string{
			filepath.Join(home, ".local", "share", "JetBrains", "Toolbox", "apps"),
			"/opt",
			filepath.Join(home, ".local", "share"),
		}
		for _, root := range roots {
			infoPaths = append(infoPaths, findFilesUpTo(root, "product-info.json", 4)...)
		}
		return infoPaths, filepath.Join(home, ".config", "JetBrains")
	default:
		return nil, ""
	}
}

// findFilesUpTo walks root up to maxDepth levels deep and returns every path whose
// base name is `name`. Bounded so a deep tree (Program Files, /opt) can't make the
// scan crawl; missing roots and permission errors are skipped.
func findFilesUpTo(root, name string, maxDepth int) []string {
	var out []string
	rootDepth := strings.Count(filepath.Clean(root), string(os.PathSeparator))
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if strings.Count(filepath.Clean(p), string(os.PathSeparator))-rootDepth > maxDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == name {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// marketplaceUpdate is the subset of the JetBrains Marketplace "updates" API
// response we need: https://plugins.jetbrains.com/api/plugins/<id>/updates?build=<build>
type marketplaceUpdate struct {
	Version string `json:"version"`
	File    string `json:"file"` // relative path, e.g. "31746/1046753/Blamely-intellij-1.0.0.zip"
}

// compatiblePluginFile asks the marketplace for an update of the Blamely
// plugin compatible with the given IDE build (e.g. "IU-261.22158.277"),
// returning the relative file path used to build the download URL. The API
// returns updates newest-first; the first entry is what the IDE's own plugin
// browser would offer.
func compatiblePluginFile(buildNumber string) (string, error) {
	url := fmt.Sprintf("https://plugins.jetbrains.com/api/plugins/%d/updates?build=%s",
		blamelyJetBrainsPluginID, buildNumber)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("query marketplace: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("marketplace returned %s", resp.Status)
	}
	var updates []marketplaceUpdate
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&updates); err != nil {
		return "", fmt.Errorf("decode marketplace response: %w", err)
	}
	for _, u := range updates {
		if u.File != "" {
			return u.File, nil
		}
	}
	return "", fmt.Errorf("no plugin build compatible with %s", buildNumber)
}

// downloadPluginZip fetches the plugin distribution zip into a temp file and
// returns its path; the caller removes it once every IDE has been processed.
// plugins.jetbrains.com/files/ 301-redirects to its CDN — http.Client follows
// redirects by default, so a plain GET is enough.
func downloadPluginZip(file string) (string, error) {
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get("https://plugins.jetbrains.com/files/" + file)
	if err != nil {
		return "", fmt.Errorf("download plugin: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download returned %s", resp.Status)
	}

	tmp, err := os.CreateTemp("", "blamely-jetbrains-plugin-*.zip")
	if err != nil {
		return "", err
	}
	defer tmp.Close()
	if _, err := io.Copy(tmp, io.LimitReader(resp.Body, 200<<20)); err != nil {
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("save plugin zip: %w", err)
	}
	return tmp.Name(), nil
}

// extractPluginZip unpacks the plugin distribution zip directly into
// pluginsDir, preserving the archive's internal layout. JetBrains plugin zips
// contain a single top-level directory (e.g. "Blamely-intellij/") that becomes
// the installed plugin's directory once it sits alongside the IDE's others.
func extractPluginZip(zipPath, pluginsDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open plugin zip: %w", err)
	}
	defer r.Close()

	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		return err
	}
	cleanRoot := filepath.Clean(pluginsDir) + string(os.PathSeparator)
	for _, f := range r.File {
		dest := filepath.Join(pluginsDir, f.Name)
		if !strings.HasPrefix(dest, cleanRoot) {
			return fmt.Errorf("plugin zip entry escapes destination: %q", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := writeZipEntry(f, dest); err != nil {
			return err
		}
	}
	return nil
}

func writeZipEntry(f *zip.File, dest string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, rc)
	return err
}
