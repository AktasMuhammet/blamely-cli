package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/blamely/blamely/internal/config"
)

// Gemini CLI's settings file lives at ~/.gemini/settings.json. Its hooks
// framework uses `AfterTool` / `BeforeTool` event names and a `matcher` field
// scoped to tool names (or "*" for all). It also requires
// `tools.enableHooks = true` to actually run hooks.
//
// Example:
//   {
//     "tools": { "enableHooks": true },
//     "hooks": {
//       "AfterTool": [
//         {
//           "matcher": "*",
//           "hooks": [ { "type": "command", "command": "/path record gemini" } ]
//         }
//       ]
//     }
//   }

const (
	geminiHookMatcher   = "*"
	geminiBlamelyMarker = "blamely record gemini"
)

// geminiHookEvents is every Gemini hook event under which we register the
// blamely record command.
var geminiHookEvents = []string{"AfterTool", "BeforeTool"}

// InstallGeminiHook merges blamely's record command into every Gemini hook
// event (BeforeTool + AfterTool) in ~/.gemini/settings.json. Idempotent.
// Preserves all unrelated keys and other tools' hook entries.
func InstallGeminiHook(binaryPath string) (added bool, settingsPath string, err error) {
	settingsPath, err = config.GeminiSettingsPath()
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

	tools := getMap(root, "tools", true)
	tools["enableHooks"] = true
	root["tools"] = tools

	hooks := getMap(root, "hooks", true)
	command := binaryPath + " record gemini"

	for _, event := range geminiHookEvents {
		groups := getSlice(hooks, event)
		if geminiAlreadyPresent(groups) {
			continue
		}
		groups = appendIntoMatcherGroup(groups, geminiHookMatcher, command)
		hooks[event] = groups
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

// UninstallGeminiHook removes ANY hook entry whose command contains
// `blamely record gemini` from every event group under settings.hooks
// (BeforeTool, AfterTool, …). User hooks and unrelated keys are preserved.
// Returns true if something was removed.
func UninstallGeminiHook() (removed bool, err error) {
	settingsPath, err := config.GeminiSettingsPath()
	if err != nil {
		return false, err
	}
	if _, statErr := os.Stat(settingsPath); errors.Is(statErr, os.ErrNotExist) {
		return false, nil
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
				if cmd != "" && containsSubstr(cmd, geminiBlamelyMarker) {
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
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshal settings: %w", err)
	}
	if err := atomicWrite(settingsPath, data, 0o644); err != nil {
		return false, err
	}
	return true, nil
}

func geminiAlreadyPresent(groups []any) bool {
	for _, g := range groups {
		grp, ok := g.(map[string]any)
		if !ok {
			continue
		}
		for _, h := range getSlice(grp, "hooks") {
			hm, _ := h.(map[string]any)
			cmd, _ := hm["command"].(string)
			if containsSubstr(cmd, geminiBlamelyMarker) {
				return true
			}
		}
	}
	return false
}
