package install

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// blamelyExtensionID is the marketplace/registry identifier for the Blamely
// extension. The same publisher.name id resolves across every VS Code-family
// gallery we target:
//   https://marketplace.visualstudio.com/items?itemName=Blamely.blamely  (VS Code)
//   https://open-vsx.org/extension/blamely/blamely                       (Open VSX — Antigravity IDE's gallery, Cursor's fallback)
const blamelyExtensionID = "Blamely.blamely"

// editorExtensionTarget describes how to locate one VS Code-family editor's
// bundled CLI so we can drive its `--install-extension` flow — the exact
// mechanism each editor's own marketplace search / "Install from VSIX" action
// uses under the hood, just invoked headlessly. Every Code-OSS fork (VS Code,
// Cursor, Antigravity IDE, …) ships this CLI and accepts the same flags
// (--list-extensions / --install-extension / --uninstall-extension); only the
// binary name and where it lives differ.
type editorExtensionTarget struct {
	Label string
	// PathNames are CLI binary names to look up on $PATH, tried in order.
	// This is the portable path — every OS's installer can wire these up.
	PathNames []string
	// AppBundles are macOS .app bundle names whose
	// Contents/Resources/app/bin/<one of PathNames> we fall back to when the
	// CLI isn't on PATH. Cursor and Antigravity IDE both ship a `cursor-alike`
	// CLI inside the bundle even when no shell command was installed.
	AppBundles []string
}

var editorExtensionTargets = []editorExtensionTarget{
	{
		Label:      "VS Code",
		PathNames:  []string{"code"},
		AppBundles: []string{"Visual Studio Code"},
	},
	{
		Label:      "Cursor",
		PathNames:  []string{"cursor"},
		AppBundles: []string{"Cursor"},
	},
	{
		Label:      "Antigravity IDE",
		PathNames:  []string{"antigravity-ide", "antigravity"},
		AppBundles: []string{"Antigravity IDE"},
	},
}

// EditorExtensionResult is one row of the install log's "Editors" group: the
// outcome of trying to get the Blamely extension into a single editor.
type EditorExtensionResult struct {
	Label     string
	CLIPath   string // "" => editor not found on this machine
	Installed bool   // true only when THIS run did the initial install (drives uninstall tracking)
	Updated   bool   // true when the extension was already present and we force-reinstalled it to pull the latest marketplace version
	Err       error
}

// AlreadyPresent reports whether the extension was found already installed
// (as opposed to freshly installed by us, absent, or failed).
func (r EditorExtensionResult) AlreadyPresent() bool {
	return r.CLIPath != "" && !r.Installed && r.Err == nil
}

// InstallEditorExtensions drives the marketplace install for every known
// VS Code-family editor present on the machine, returning one result per
// target regardless of outcome so the caller can render a single consistent
// "Editors" group (found-and-installed / found-and-already-there / absent /
// failed).
func InstallEditorExtensions() []EditorExtensionResult {
	results := make([]EditorExtensionResult, 0, len(editorExtensionTargets))
	for _, t := range editorExtensionTargets {
		cliPath, _ := findEditorCLI(t)
		r := EditorExtensionResult{Label: t.Label, CLIPath: cliPath}
		if cliPath == "" {
			results = append(results, r)
			continue
		}
		// Force-reinstall unconditionally: a fresh marketplace pull both
		// installs it the first time and updates it to latest on every
		// subsequent run, so users never get stuck on a stale version.
		wasPresent := extensionInstalled(cliPath, blamelyExtensionID)
		out, err := exec.Command(cliPath, "--install-extension", blamelyExtensionID, "--force").CombinedOutput()
		if err != nil {
			r.Err = fmt.Errorf("%s --install-extension %s --force: %w: %s",
				filepath.Base(cliPath), blamelyExtensionID, err, strings.TrimSpace(string(out)))
		} else if wasPresent {
			r.Updated = true
		} else {
			r.Installed = true
		}
		results = append(results, r)
	}
	return results
}

// UninstallEditorExtensions removes the Blamely extension from every editor
// labelled in `labels` — the set we recorded as "this run installed it" at
// install time. We never remove an extension the user installed themselves;
// that would be a surprising, hard-to-reverse side effect of `blamely
// uninstall`. Best-effort: an editor that vanished since install is skipped.
func UninstallEditorExtensions(labels []string) error {
	if len(labels) == 0 {
		return nil
	}
	want := make(map[string]bool, len(labels))
	for _, l := range labels {
		want[l] = true
	}
	var firstErr error
	for _, t := range editorExtensionTargets {
		if !want[t.Label] {
			continue
		}
		cliPath, ok := findEditorCLI(t)
		if !ok {
			continue
		}
		if err := exec.Command(cliPath, "--uninstall-extension", blamelyExtensionID).Run(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// findEditorCLI locates an editor's bundled CLI: first on $PATH (the portable
// path — most installers wire up a shell command there), then — on macOS —
// inside the app bundle's Contents/Resources/app/bin/, where Code-OSS forks
// place their CLI launcher even when no shell command was set up.
func findEditorCLI(t editorExtensionTarget) (string, bool) {
	for _, name := range t.PathNames {
		if p, ok := lookPath(name); ok {
			return p, true
		}
	}
	if runtime.GOOS != "darwin" {
		return "", false
	}
	for _, app := range t.AppBundles {
		base := filepath.Join("/Applications", app+".app", "Contents", "Resources", "app", "bin")
		for _, name := range t.PathNames {
			p := filepath.Join(base, name)
			if fileExists(p) {
				return p, true
			}
		}
	}
	return "", false
}

func extensionInstalled(cliPath, id string) bool {
	out, err := exec.Command(cliPath, "--list-extensions").Output()
	if err != nil {
		return false
	}
	target := strings.ToLower(id)
	for _, line := range strings.Split(string(out), "\n") {
		if strings.ToLower(strings.TrimSpace(line)) == target {
			return true
		}
	}
	return false
}
