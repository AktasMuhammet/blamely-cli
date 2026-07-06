package install

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/blamely/blamely/internal/config"
)

// Codex's config lives at ~/.codex/config.toml. Hooks are enabled by setting
// `[features] hooks = true` and then declaring one or more `[[hooks.PostToolUse]]`
// groups, each with a `[[hooks.PostToolUse.hooks]]` array of `{ command, type }`.
//
// Example:
//   [features]
//   hooks = true
//
//   [[hooks.PostToolUse]]
//
//   [[hooks.PostToolUse.hooks]]
//   command = "/path/to/blamely record codex"
//   type = "command"

// Marker omits the binary name: the command is `<path> record codex`, and on
// Windows <path> ends in `blamely.exe`, so "blamely record codex" would never
// match. `record codex` is the extension-agnostic tail that's always present.
const codexBlamelyMarker = "record codex"

// codexHookEvents is every Codex hook event under which we register the
// blamely record command.
var codexHookEvents = []string{"PostToolUse", "PreToolUse"}

// InstallCodexHook merges blamely's record-hook into every Codex event
// (PreToolUse + PostToolUse), across EVERY Codex home in the union (~/.codex and any
// custom CODEX_HOME / corp dir that exists) — never just the default. When none
// exist yet it creates the hook in the home default. Idempotent. Preserves all
// unrelated keys, including hooks.state.* trust entries Codex manages.
func InstallCodexHook(binaryPath string) (added bool, configPath string, err error) {
	targets := codexHookTargets()
	written := make([]string, 0, len(targets))
	for _, p := range targets {
		a, werr := installCodexHookAt(p, binaryPath)
		if werr != nil {
			return added, strings.Join(append(written, p), ", "), werr
		}
		written = append(written, p)
		added = added || a
	}
	return added, strings.Join(written, ", "), nil
}

// codexHookTargets is the set of config.toml paths to install into: every union
// base whose dir already exists, or the home default if none do yet.
func codexHookTargets() []string {
	var out []string
	for _, p := range config.CodexConfigPaths() {
		if dirExists(filepath.Dir(p)) {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		if def, err := config.CodexConfigPath(); err == nil {
			out = append(out, def)
		}
	}
	return out
}

func installCodexHookAt(configPath, binaryPath string) (added bool, err error) {
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return false, fmt.Errorf("mkdir %s: %w", filepath.Dir(configPath), err)
	}

	root, err := readTOML(configPath)
	if err != nil {
		return false, err
	}
	before := canonJSON(root)

	features := getMap(root, "features", true)
	features["hooks"] = true
	root["features"] = features

	hooks := getMap(root, "hooks", true)
	command := recordHookCommand(binaryPath, "codex")

	for _, event := range codexHookEvents {
		groups := getSlice(hooks, event)
		// Strip any existing blamely group first (dedupe repeats / drop a stale
		// binary path), then prepend a single fresh one so blamely runs first —
		// a third-party hook ordered ahead of us that errors out can abort the
		// rest of the chain and stop blamely from recording the edit.
		groups = stripBlamelyMatcherGroups(groups, codexBlamelyMarker)
		blamelyGroup := map[string]any{
			"hooks": []any{
				map[string]any{"command": command, "type": "command"},
			},
		}
		groups = append([]any{blamelyGroup}, groups...)
		hooks[event] = groups
	}
	root["hooks"] = hooks

	if canonJSON(root) == before {
		return false, nil
	}

	if err := writeTOML(configPath, root); err != nil {
		return false, err
	}
	return true, nil
}

// UninstallCodexHook removes ANY hook entry whose command contains
// `blamely record codex` from every event group under hooks (PreToolUse,
// PostToolUse, …), across EVERY Codex home in the union (default + custom).
// hooks.state.* trust entries set by Codex are preserved. Returns true if
// something was removed from any location.
func UninstallCodexHook() (removed bool, err error) {
	seen := map[string]bool{}
	paths := config.CodexConfigPaths()
	if def, derr := config.CodexConfigPath(); derr == nil {
		paths = append(paths, def)
	}
	for _, p := range paths {
		if seen[p] {
			continue
		}
		seen[p] = true
		r, uerr := uninstallCodexHookAt(p)
		if uerr != nil {
			return removed, uerr
		}
		removed = removed || r
	}
	return removed, nil
}

func uninstallCodexHookAt(configPath string) (removed bool, err error) {
	root, err := readTOML(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	hooks := getMap(root, "hooks", false)
	if hooks == nil {
		return false, nil
	}

	for event, raw := range hooks {
		// hooks.state is a map of trust entries, NOT a list of hook groups.
		// Skip non-array values.
		groups, ok := raw.([]any)
		if !ok {
			continue
		}
		newGroups := groups[:0]
		changed := false
		for _, g := range groups {
			grp, ok := g.(map[string]any)
			if !ok {
				newGroups = append(newGroups, g)
				continue
			}
			inner := getSlice(grp, "hooks")
			filtered := inner[:0]
			for _, h := range inner {
				hm, _ := h.(map[string]any)
				cmd, _ := hm["command"].(string)
				if cmd != "" && containsSubstr(cmd, codexBlamelyMarker) {
					removed = true
					changed = true
					continue
				}
				filtered = append(filtered, h)
			}
			if len(filtered) == 0 {
				changed = true
				continue
			}
			grp["hooks"] = filtered
			newGroups = append(newGroups, grp)
		}
		if !changed {
			continue
		}
		if len(newGroups) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = newGroups
		}
	}
	if !removed {
		return false, nil
	}
	if len(hooks) == 0 {
		delete(root, "hooks")
	} else {
		root["hooks"] = hooks
	}
	if err := writeTOML(configPath, root); err != nil {
		return false, err
	}
	return true, nil
}

func readTOML(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	var root map[string]any
	if err := toml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if root == nil {
		return map[string]any{}, nil
	}
	// BurntSushi/toml decodes `[[array.of.tables]]` as []map[string]any, but
	// the rest of the install package walks structures using []any. Normalize
	// so getSlice/getMap helpers (shared with the JSON installers) work.
	return normalizeTOML(root).(map[string]any), nil
}

func normalizeTOML(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, vv := range x {
			x[k] = normalizeTOML(vv)
		}
		return x
	case []map[string]any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalizeTOML(e)
		}
		return out
	case []any:
		for i, e := range x {
			x[i] = normalizeTOML(e)
		}
		return x
	default:
		return v
	}
}

func writeTOML(path string, root map[string]any) error {
	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	enc.Indent = ""
	if err := enc.Encode(root); err != nil {
		return fmt.Errorf("marshal toml: %w", err)
	}
	return atomicWrite(path, buf.Bytes(), 0o644)
}
