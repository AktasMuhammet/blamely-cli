package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/blamely/blamely/internal/config"
)

// Cursor's hooks file lives at ~/.cursor/hooks.json and has the shape:
//   {
//     "version": 1,
//     "hooks": {
//       "postToolUse": [ { "command": "/path/to/tool --hook-input stdin" } ]
//     }
//   }
// Cursor uses lowercase event names (postToolUse / preToolUse) and no matcher.

const cursorBlamelyMarker = "blamely record cursor"

// cursorHookEvents is every event under which we register the blamely record
// command in ~/.cursor/hooks.json.
//
// preToolUse is intentionally excluded. At preToolUse time the AI has not yet
// applied its edit, so RecordClaudeFromStdin would call LineRangeForWholeFile
// on the OLD file content and record a whole-file cursor/chat row that covers
// any lines the user typed manually since the previous commit. That row lands
// with a timestamp after the humanedit rows and wins attribution at commit
// time, making human-typed code show up as AI. postToolUse fires after the
// edit has landed and captures the correct line ranges — no benefit from
// preToolUse.
var cursorHookEvents = []string{"postToolUse"}

// InstallCursorHook merges blamely's record command into every Cursor hook
// event (postToolUse + preToolUse) in ~/.cursor/hooks.json. Idempotent.
// Preserves all unrelated keys (including other tools' hooks like git-ai).
func InstallCursorHook(binaryPath string) (added bool, hooksPath string, err error) {
	hooksPath, err = config.CursorHooksPath()
	if err != nil {
		return false, "", err
	}
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		return false, hooksPath, fmt.Errorf("mkdir %s: %w", filepath.Dir(hooksPath), err)
	}

	root, err := readSettings(hooksPath)
	if err != nil {
		return false, hooksPath, err
	}

	if _, ok := root["version"]; !ok {
		root["version"] = float64(1)
	}

	hooks := getMap(root, "hooks", true)
	command := binaryPath + " record cursor"

	for _, event := range cursorHookEvents {
		entries := getSlice(hooks, event)
		if cursorAlreadyPresent(entries) {
			continue
		}
		entries = append(entries, map[string]any{"command": command})
		hooks[event] = entries
		added = true
	}
	root["hooks"] = hooks

	if !added {
		return false, hooksPath, nil
	}

	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return false, hooksPath, fmt.Errorf("marshal hooks: %w", err)
	}
	if err := atomicWrite(hooksPath, data, 0o644); err != nil {
		return false, hooksPath, err
	}
	return true, hooksPath, nil
}

// UninstallCursorHook removes any entry whose command contains
// `blamely record cursor` from every event under hooks (postToolUse,
// preToolUse, …). User hooks and unrelated keys are preserved.
// Returns true if something was removed.
func UninstallCursorHook() (removed bool, err error) {
	hooksPath, err := config.CursorHooksPath()
	if err != nil {
		return false, err
	}
	root, err := readSettings(hooksPath)
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
		entries, ok := raw.([]any)
		if !ok {
			continue
		}
		filtered := entries[:0]
		changed := false
		for _, h := range entries {
			hm, _ := h.(map[string]any)
			cmd, _ := hm["command"].(string)
			if cmd != "" && containsCursorBlamelyHook(cmd) {
				removed = true
				changed = true
				continue
			}
			filtered = append(filtered, h)
		}
		if !changed {
			continue
		}
		if len(filtered) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = filtered
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
		return false, fmt.Errorf("marshal hooks: %w", err)
	}
	if err := atomicWrite(hooksPath, data, 0o644); err != nil {
		return false, err
	}
	return true, nil
}

func cursorAlreadyPresent(entries []any) bool {
	for _, h := range entries {
		hm, _ := h.(map[string]any)
		cmd, _ := hm["command"].(string)
		if containsCursorBlamelyHook(cmd) {
			return true
		}
	}
	return false
}

func containsCursorBlamelyHook(cmd string) bool {
	return containsSubstr(cmd, cursorBlamelyMarker)
}

// containsSubstr is a small, dependency-free substring check used by all the
// hook installers to detect prior blamely entries in user-provided commands.
func containsSubstr(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
