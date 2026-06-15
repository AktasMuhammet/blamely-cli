package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/blamely/blamely/internal/config"
)

// GitHub Copilot's hooks framework loads any JSON file from ~/.copilot/hooks/.
// Blamely owns the file ~/.copilot/hooks/blamely.json. Other tools (e.g.,
// git-ai) drop their own JSON file alongside ours and are never touched.
//
// File shape:
//   {
//     "hooks": {
//       "PostToolUse": [
//         { "command": "/path/to/blamely record copilot", "type": "command" }
//       ]
//     }
//   }

// Marker omits the binary name: the command is `<path> record copilot`, and on
// Windows <path> ends in `blamely.exe`, so "blamely record copilot" would never
// match. `record copilot` is the extension-agnostic tail that's always present.
const copilotBlamelyMarker = "record copilot"

// copilotHookEvents is every event we register the blamely command under
// inside our dedicated blamely.json file.
var copilotHookEvents = []string{"PostToolUse", "PreToolUse"}

// InstallCopilotHook writes (or merges) blamely's record command into every
// Copilot hook event in ~/.copilot/hooks/blamely.json. Idempotent. Doesn't
// touch sibling files from other tools (e.g. git-ai.json).
func InstallCopilotHook(binaryPath string) (added bool, hookPath string, err error) {
	hookPath, err = config.CopilotBlamelyHookPath()
	if err != nil {
		return false, "", err
	}
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		return false, hookPath, fmt.Errorf("mkdir %s: %w", filepath.Dir(hookPath), err)
	}

	root, err := readSettings(hookPath)
	if err != nil {
		return false, hookPath, err
	}
	before := canonJSON(root)

	hooks := getMap(root, "hooks", true)
	command := recordHookCommand(binaryPath, "copilot")

	for _, event := range copilotHookEvents {
		entries := getSlice(hooks, event)
		// Strip any existing blamely entry (dedupe / drop stale path), then
		// prepend a single fresh one first — a failing third-party hook ordered
		// ahead of us must not abort the chain before blamely records the edit.
		entries = stripBlamelyEntries(entries, copilotBlamelyMarker)
		entries = append([]any{map[string]any{
			"command": command,
			"type":    "command",
		}}, entries...)
		hooks[event] = entries
	}
	root["hooks"] = hooks

	if canonJSON(root) == before {
		return false, hookPath, nil
	}

	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return false, hookPath, fmt.Errorf("marshal hooks: %w", err)
	}
	if err := atomicWrite(hookPath, data, 0o644); err != nil {
		return false, hookPath, err
	}
	return true, hookPath, nil
}

// UninstallCopilotHook removes the blamely.json file we wrote. If it contains
// only our entries, the file is deleted. If a user added unrelated entries,
// only our entries are stripped and the file is kept.
func UninstallCopilotHook() (removed bool, err error) {
	hookPath, err := config.CopilotBlamelyHookPath()
	if err != nil {
		return false, err
	}
	if _, statErr := os.Stat(hookPath); errors.Is(statErr, os.ErrNotExist) {
		return false, nil
	}
	root, err := readSettings(hookPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	hooks := getMap(root, "hooks", false)
	if hooks == nil {
		// File exists but has no hooks block — leave user content alone.
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
			if cmd != "" && containsSubstr(cmd, copilotBlamelyMarker) {
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
	// If the file would now be empty after stripping our entries, delete it.
	if len(root) == 0 {
		if err := os.Remove(hookPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		return true, nil
	}
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshal hooks: %w", err)
	}
	if err := atomicWrite(hookPath, data, 0o644); err != nil {
		return false, err
	}
	return true, nil
}
