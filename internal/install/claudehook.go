package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/blamely/blamely/internal/config"
)

const (
	// Bash is included so file writes Claude performs via shell (e.g.
	// `printf > f`, `cat > f`, heredocs, scripts) — which bypass the
	// Write/Edit tools — still fire our PostToolUse hook. The recorder
	// attributes the source files that changed during the command.
	claudeHookMatcher = "Write|Edit|MultiEdit|NotebookEdit|Bash"
	// Marker omits the binary name on purpose: the command is
	// `<path> record claude`, and on Windows <path> ends in `blamely.exe`, so a
	// "blamely record claude" needle would never match. `record claude` is the
	// extension-agnostic, blamely-specific tail that's always present.
	blamelyHookMarker = "record claude"
)

// claudeHookEvents is every event name we register the blamely command under.
// Adding to this list automatically gets the install path; uninstall scans
// the full `hooks` map regardless and will still find our markers.
//
// PreToolUse is intentionally excluded — see the matching comment in
// cursorhook.go for the full explanation. Short version: at PreToolUse time
// Write operations record the OLD file content as an AI claim, covering
// human-typed lines and making them appear as AI in subsequent commits.
var claudeHookEvents = []string{"PostToolUse"}

// InstallClaudeHook merges blamely's record-hook into every event we care
// about under ~/.claude/settings.json (PostToolUse today).
// Idempotent. Preserves all unrelated keys (other matchers, user hooks,
// settings unrelated to hooks). Returns true if anything new was added.
func InstallClaudeHook(binaryPath string) (added bool, settingsPath string, err error) {
	settingsPath, err = config.ClaudeSettingsPath()
	if err != nil {
		return false, "", err
	}
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return false, settingsPath, fmt.Errorf("mkdir %s: %w", filepath.Dir(settingsPath), err)
	}

	root, err := readSettings(settingsPath)
	if err != nil {
		return false, settingsPath, err
	}

	hooks := getMap(root, "hooks", true)
	command := binaryPath + " record claude"

	for _, event := range claudeHookEvents {
		entries := getSlice(hooks, event)
		// Migrate older installs whose blamely group used a narrower matcher
		// (e.g. one without Bash) up to the current matcher.
		if migrateClaudeMatcher(entries, claudeHookMatcher) {
			hooks[event] = entries
			added = true
		}
		if alreadyPresent(entries) {
			continue
		}
		entries = appendIntoMatcherGroup(entries, claudeHookMatcher, command)
		hooks[event] = entries
		added = true
	}
	root["hooks"] = hooks

	if !added {
		return false, settingsPath, nil
	}

	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return false, settingsPath, fmt.Errorf("marshal settings: %w", err)
	}
	if err := atomicWrite(settingsPath, data, 0o644); err != nil {
		return false, settingsPath, err
	}
	return true, settingsPath, nil
}

// migrateClaudeMatcher updates the `matcher` of any existing group that contains
// a blamely hook but whose matcher differs from the current one (e.g. an older
// install that didn't watch Bash). Returns true if it changed anything. This
// keeps `blamely install` idempotent while still upgrading legacy hook entries.
func migrateClaudeMatcher(groups []any, matcher string) bool {
	changed := false
	for _, g := range groups {
		grp, ok := g.(map[string]any)
		if !ok {
			continue
		}
		if m, _ := grp["matcher"].(string); m == matcher {
			continue
		}
		inner := getSlice(grp, "hooks")
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			if cmd, _ := hm["command"].(string); containsBlamelyHook(cmd) {
				grp["matcher"] = matcher
				changed = true
				break
			}
		}
	}
	return changed
}

// appendIntoMatcherGroup adds {type:command, command} to the matcher group
// whose `matcher` equals `matcher`; creates the group if none exists.
func appendIntoMatcherGroup(groups []any, matcher, command string) []any {
	for i, g := range groups {
		grp, ok := g.(map[string]any)
		if !ok {
			continue
		}
		if m, _ := grp["matcher"].(string); m != matcher {
			continue
		}
		inner := getSlice(grp, "hooks")
		inner = append(inner, map[string]any{
			"type":    "command",
			"command": command,
		})
		grp["hooks"] = inner
		groups[i] = grp
		return groups
	}
	return append(groups, map[string]any{
		"matcher": matcher,
		"hooks": []any{
			map[string]any{"type": "command", "command": command},
		},
	})
}

// UninstallClaudeHook removes ANY hook entry whose command contains
// `blamely record claude` from every event group under settings.hooks
// (PreToolUse, PostToolUse, etc.). User hooks and unrelated keys are
// preserved. Returns true if something was removed.
func UninstallClaudeHook() (removed bool, err error) {
	settingsPath, err := config.ClaudeSettingsPath()
	if err != nil {
		return false, err
	}
	root, err := readSettings(settingsPath)
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
		groups, ok := raw.([]any)
		if !ok {
			continue
		}
		newGroups := groups[:0]
		eventChanged := false
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
				if cmd != "" && containsBlamelyHook(cmd) {
					removed = true
					eventChanged = true
					continue
				}
				filtered = append(filtered, h)
			}
			if len(filtered) == 0 {
				eventChanged = true
				continue
			}
			grp["hooks"] = filtered
			newGroups = append(newGroups, grp)
		}
		if !eventChanged {
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
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshal settings: %w", err)
	}
	if err := atomicWrite(settingsPath, data, 0o644); err != nil {
		return false, err
	}
	return true, nil
}

func readSettings(path string) (map[string]any, error) {
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
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if root == nil {
		return map[string]any{}, nil
	}
	return root, nil
}

func getMap(parent map[string]any, key string, create bool) map[string]any {
	if v, ok := parent[key].(map[string]any); ok {
		return v
	}
	if !create {
		return nil
	}
	m := map[string]any{}
	parent[key] = m
	return m
}

func getSlice(parent map[string]any, key string) []any {
	if v, ok := parent[key].([]any); ok {
		return v
	}
	return nil
}

func alreadyPresent(groups []any) bool {
	for _, g := range groups {
		grp, ok := g.(map[string]any)
		if !ok {
			continue
		}
		inner := getSlice(grp, "hooks")
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			cmd, _ := hm["command"].(string)
			if containsBlamelyHook(cmd) {
				return true
			}
		}
	}
	return false
}

func containsBlamelyHook(cmd string) bool {
	// Match any path ending in `... record claude`.
	for i := 0; i+len(blamelyHookMarker) <= len(cmd); i++ {
		if cmd[i:i+len(blamelyHookMarker)] == blamelyHookMarker {
			return true
		}
	}
	return false
}
