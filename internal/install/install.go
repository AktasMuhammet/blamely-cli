package install

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/blamely/blamely/internal/config"
	"github.com/blamely/blamely/internal/daemon"
)

// Run is the orchestrator behind `blamely install`. It:
//   1. Detects which AI tools are present on the machine.
//   2. Resolves the absolute path of the running blamely binary.
//   3. Merges a `blamely record <tool>` PostToolUse hook into each detected
//      tool's settings file (Claude, Cursor, Codex, Copilot). Existing hooks
//      from the user or other tools are preserved.
//   4. Sets `git config --global core.hooksPath` to the Blamely hooks dir and
//      writes a post-commit script that calls `blamely attribute`.
//   5. Registers the daemon under launchd / systemd --user / Scheduled Tasks.
//   6. Persists a state.json so `uninstall` can fully reverse the install.
func Run() error {
	srcBinPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate blamely binary: %w", err)
	}

	// Copy the binary to ~/.blamely/bin/ so the post-commit hook and the OS
	// agent reference a stable path that doesn't break if the user moves or
	// re-installs the dev binary.
	binPath, err := CopyBinary(srcBinPath)
	if err != nil {
		return fmt.Errorf("install binary: %w", err)
	}

	detected, err := Detect()
	if err != nil {
		return err
	}

	printDetected(detected)
	info("Binary", binPath)

	s, err := LoadState()
	if err != nil {
		return err
	}
	if s == nil {
		s = &State{}
	}
	s.InstalledAt = time.Now()
	s.BinaryPath = binPath

	// Hooks: a `blamely record <tool>` hook merged into each detected AI
	// tool's own settings/config file, plus the global git post-commit hook
	// that turns recorded edits into per-commit attribution notes. Grouped
	// together because they're the same mechanism (config-file merge) and the
	// same thing a user would look for when checking "is blamely wired up?".
	section("Hooks")

	if detected.Claude.Present {
		added, settingsPath, err := InstallClaudeHook(binPath)
		if err != nil {
			return fmt.Errorf("claude hook: %w", err)
		}
		if added {
			ok("Claude Code", settingsPath)
		} else {
			info("Claude Code", "hook already present · "+settingsPath)
		}
		s.ClaudeHookAdded = true
	} else {
		info("Claude Code", "not detected — skipped")
	}

	if detected.Cursor.Present {
		added, hooksPath, err := InstallCursorHook(binPath)
		if err != nil {
			return fmt.Errorf("cursor hook: %w", err)
		}
		if added {
			ok("Cursor", hooksPath)
		} else {
			info("Cursor", "hook already present · "+hooksPath)
		}
		s.CursorHookAdded = true
	} else {
		info("Cursor", "not detected — skipped")
	}

	if detected.Codex.Present {
		added, configPath, err := InstallCodexHook(binPath)
		if err != nil {
			return fmt.Errorf("codex hook: %w", err)
		}
		if added {
			ok("Codex CLI", configPath)
		} else {
			info("Codex CLI", "hook already present · "+configPath)
		}
		s.CodexHookAdded = true
	} else {
		info("Codex CLI", "not detected — skipped")
	}

	if detected.Copilot.Present {
		added, hookPath, err := InstallCopilotHook(binPath)
		if err != nil {
			return fmt.Errorf("copilot hook: %w", err)
		}
		if added {
			ok("GitHub Copilot", hookPath)
		} else {
			info("GitHub Copilot", "hook already present · "+hookPath)
		}
		s.CopilotHookAdded = true
	} else {
		info("GitHub Copilot", "not detected — skipped")
	}

	if detected.Gemini.Present {
		added, settingsPath, err := InstallGeminiHook(binPath)
		if err != nil {
			return fmt.Errorf("gemini hook: %w", err)
		}
		if added {
			ok("Gemini CLI", settingsPath)
		} else {
			info("Gemini CLI", "hook already present · "+settingsPath)
		}
		s.GeminiHookAdded = true
	} else {
		info("Gemini CLI", "not detected — skipped")
	}

	prior, hadPrior, err := InstallGitHook(binPath)
	if err != nil {
		return fmt.Errorf("git hook: %w", err)
	}
	s.PriorCoreHooksPath = prior
	s.HadCoreHooksPath = hadPrior
	s.GitHookInstalled = true
	if hadPrior {
		ok("Git post-commit", fmt.Sprintf("global hook installed · previous core.hooksPath %q stashed", prior))
	} else {
		ok("Git post-commit", "global hook installed")
	}

	// Editors: marketplace-distributed extensions that give a VS Code-family
	// editor its own attribution surface (chat-panel detection, inline UI, …),
	// auto-installed via each editor's bundled CLI when the editor is present.
	// Separate from Hooks because these come from an external marketplace
	// (VS Code Marketplace / Open VSX) rather than a config-file merge.
	section("Editors")

	var editorLabelsInstalled []string
	for _, r := range InstallEditorExtensions() {
		switch {
		case r.Err != nil:
			fail(r.Label, r.Err.Error())
		case r.CLIPath == "":
			info(r.Label, "not detected — skipped")
		case r.Installed:
			ok(r.Label, "extension installed from marketplace · "+blamelyExtensionID)
			editorLabelsInstalled = append(editorLabelsInstalled, r.Label)
		case r.Updated:
			ok(r.Label, "extension updated to latest · "+blamelyExtensionID)
		default:
			info(r.Label, "extension already installed · "+blamelyExtensionID)
		}
	}
	s.EditorExtensionsInstalled = mergeLabels(s.EditorExtensionsInstalled, editorLabelsInstalled)

	// JetBrains IDEs (IntelliJ IDEA, WebStorm, GoLand, …) don't expose a CLI
	// extension-install flow the way Code-OSS forks do, so we go straight to
	// the JetBrains Marketplace: download a build-compatible plugin zip and
	// unzip it into the IDE's plugins directory.
	jetResults := InstallJetBrainsPlugins()
	if len(jetResults) == 0 {
		info("JetBrains IDEs", "not detected — skipped")
	} else {
		var jetbrainsRestartNeeded bool
		var jetbrainsDirsInstalled []string
		for _, r := range jetResults {
			switch {
			case r.Err != nil:
				fail(r.Label, r.Err.Error())
			case r.Installed:
				ok(r.Label, "plugin installed from marketplace · ai.blamely")
				jetbrainsDirsInstalled = append(jetbrainsDirsInstalled, r.PluginsDir)
				jetbrainsRestartNeeded = true
			default:
				info(r.Label, "plugin already installed · ai.blamely")
			}
		}
		s.JetBrainsPluginsInstalled = mergeLabels(s.JetBrainsPluginsInstalled, jetbrainsDirsInstalled)
		if jetbrainsRestartNeeded {
			info("JetBrains IDEs", "restart to load the newly installed plugin")
		}
	}

	// System: the background daemon that receives hook events, the shell PATH
	// entry, and the default config/exclude files that shape what attribution
	// looks like. The plumbing that makes the above two groups actually work.
	section("System")

	// We invalidate any stale port file BEFORE registering the agent so the
	// post-install health check can't false-positive on the previous daemon's
	// port. Then we register/restart and wait for the new daemon to come up.
	if portPath, perr := config.PortFile(); perr == nil {
		_ = os.Remove(portPath)
	}
	agentRef, err := InstallDaemonAgent(binPath)
	if err != nil {
		return fmt.Errorf("daemon agent: %w", err)
	}
	s.LaunchAgentInstalled = true
	ok("Daemon agent", agentRef)

	// Block until the daemon actually answers /health, so the user knows
	// hooks are being listened to before this command exits.
	if port, derr := daemon.WaitForReady(8 * time.Second); derr != nil {
		diagnoseDaemon(derr, agentRef)
	} else {
		ok("Daemon", fmt.Sprintf("listening on 127.0.0.1:%d · ready to receive hooks", port))
	}

	// Best-effort — if shell detection fails or the rc isn't writable, we
	// print a manual hint instead of failing the install (the binary is still
	// on disk and the daemon is still wired up).
	if rcPath, added, perr := InstallPathEntry(); perr != nil {
		fail("PATH", fmt.Sprintf("could not auto-add ~/.blamely/bin: %v", perr))
	} else {
		s.PathRcFile = rcPath
		if added {
			s.PathEntryAdded = true
			if runtime.GOOS == "windows" {
				ok("PATH", fmt.Sprintf("added %s to user PATH · open a new terminal", rcPath))
			} else {
				ok("PATH", fmt.Sprintf("added to %s · reload your shell or run `source %s`", rcPath, rcPath))
			}
		} else {
			if runtime.GOOS == "windows" {
				info("PATH", "entry already present in user PATH ("+rcPath+")")
			} else {
				info("PATH", "entry already present in "+rcPath)
			}
		}
	}

	// Default exclude/config files. We never overwrite an existing one —
	// users edit these to customise what's skipped from attribution and what
	// each commit's git note includes, and a fresh install must keep their
	// edits intact.
	if excludePath, created, eerr := config.EnsureDefaultExcludeFile(); eerr != nil {
		fail("Exclude list", eerr.Error())
	} else if created {
		ok("Exclude list", excludePath)
	} else {
		info("Exclude list", "already present · "+excludePath)
	}

	if configPath, created, cerr := config.EnsureDefaultConfigFile(); cerr != nil {
		fail("Config", cerr.Error())
	} else if created {
		ok("Config", configPath)
	} else {
		info("Config", "already present · "+configPath)
	}

	if err := SaveState(s); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	fmt.Println()
	printNextSteps(detected)
	return nil
}

func Uninstall() error {
	s, err := LoadState()
	if err != nil {
		return err
	}

	var firstErr error
	report := func(label string, err error) {
		if err == nil {
			fmt.Printf("  ✓ %s\n", label)
			return
		}
		fmt.Printf("  ✗ %s: %v\n", label, err)
		if firstErr == nil {
			firstErr = err
		}
	}

	if s.GitHookInstalled || s.HadCoreHooksPath {
		report("removed global git post-commit hook", UninstallGitHook(s.PriorCoreHooksPath, s.HadCoreHooksPath))
	}
	if s.ClaudeHookAdded {
		_, err := UninstallClaudeHook()
		report("removed Claude record hook from ~/.claude/settings.json", err)
	}
	if s.CursorHookAdded {
		_, err := UninstallCursorHook()
		report("removed Cursor record hook from ~/.cursor/hooks.json", err)
	}
	if s.CodexHookAdded {
		_, err := UninstallCodexHook()
		report("removed Codex record hook from ~/.codex/config.toml", err)
	}
	if s.CopilotHookAdded {
		_, err := UninstallCopilotHook()
		report("removed Copilot record hook from ~/.copilot/hooks/blamely.json", err)
	}
	if s.GeminiHookAdded {
		_, err := UninstallGeminiHook()
		report("removed Gemini record hook from ~/.gemini/settings.json", err)
	}
	if len(s.EditorExtensionsInstalled) > 0 {
		report(fmt.Sprintf("removed Blamely extension from %s", strings.Join(s.EditorExtensionsInstalled, ", ")),
			UninstallEditorExtensions(s.EditorExtensionsInstalled))
	}
	if len(s.JetBrainsPluginsInstalled) > 0 {
		report(fmt.Sprintf("removed Blamely plugin from %d JetBrains IDE(s)", len(s.JetBrainsPluginsInstalled)),
			UninstallJetBrainsPlugins(s.JetBrainsPluginsInstalled))
	}
	if s.LaunchAgentInstalled {
		report("removed daemon agent", UninstallDaemonAgent())
	}
	if s.PathEntryAdded {
		rcPath, _, err := UninstallPathEntry(s.PathRcFile)
		report(fmt.Sprintf("removed PATH entry from %s", rcPath), err)
	}

	// Remove the stable binary copy.
	if p, err := InstalledBinaryPath(); err == nil {
		_ = os.Remove(p)
		_ = os.Remove(filepath.Dir(p)) // empty bin/ dir
	}
	// Wipe state.json last.
	if statePath, err := config.StateFile(); err == nil {
		_ = os.Remove(statePath)
	}

	if firstErr != nil {
		return firstErr
	}
	fmt.Println()
	fmt.Println("Blamely uninstalled. SQLite database at ~/.blamely/db.sqlite was left in place.")
	fmt.Println("Remove it manually if you want to wipe all attribution history.")
	return nil
}

// mergeLabels folds freshly-installed editor labels into the persisted set,
// de-duplicating. Re-running `install` after an editor was added shouldn't
// drop the editors recorded by earlier runs.
func mergeLabels(existing, fresh []string) []string {
	seen := make(map[string]bool, len(existing)+len(fresh))
	out := make([]string, 0, len(existing)+len(fresh))
	for _, l := range append(append([]string{}, existing...), fresh...) {
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}

func printDetected(d *Detected) {
	if uiColor() {
		fmt.Printf("%sDetected%s\n", uiBold, uiReset)
	} else {
		fmt.Println("Detected")
	}
	for _, row := range []struct {
		name string
		p    ToolPresence
	}{
		{"Claude Code", d.Claude},
		{"Cursor", d.Cursor},
		{"Codex CLI", d.Codex},
		{"GitHub Copilot", d.Copilot},
		{"Gemini CLI", d.Gemini},
	} {
		hint := ""
		if h := row.p.FirstHint(); h != "" {
			hint = h
			if more := len(row.p.Hints) - 1; more > 0 {
				hint += fmt.Sprintf("  (+%d more)", more)
			}
		}
		if row.p.Present {
			ok(row.name, hint)
		} else {
			info(row.name, "not detected")
		}
	}
}

func printNextSteps(d *Detected) {
	section("Next steps")
	if d.Claude.Present {
		fmt.Println("  · Make an edit with Claude Code, then commit. Run `blamely report HEAD` to see the per-line attribution.")
	} else {
		fmt.Println("  · Claude Code wasn't detected. Install it, run `blamely install` again, or add the hook manually.")
	}
	if !d.Cursor.Present && !d.Codex.Present && !d.Copilot.Present && !d.Gemini.Present {
		fmt.Println("  · Cursor/Codex/Copilot/Gemini integrations will activate automatically once those tools appear.")
	}
	fmt.Println("  · `blamely status` shows the daemon health.")
	fmt.Println("  · `blamely uninstall` reverses every change above.")
}

