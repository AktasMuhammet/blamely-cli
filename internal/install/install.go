package install

import (
	"fmt"
	"os"
	"path/filepath"
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
	fmt.Printf("  binary stable path: %s\n\n", binPath)

	s, err := LoadState()
	if err != nil {
		return err
	}
	if s == nil {
		s = &State{}
	}
	s.InstalledAt = time.Now()
	s.BinaryPath = binPath

	// 1. AI tool hooks (only for tools that are actually detected).
	if detected.Claude.Present {
		added, settingsPath, err := InstallClaudeHook(binPath)
		if err != nil {
			return fmt.Errorf("claude hook: %w", err)
		}
		if added {
			fmt.Printf("  ✓ Claude hook installed at %s\n", settingsPath)
		} else {
			fmt.Printf("  • Claude hook already present at %s\n", settingsPath)
		}
		s.ClaudeHookAdded = true
	} else {
		fmt.Println("  • Claude Code not detected — skipping settings.json hook")
	}

	if detected.Cursor.Present {
		added, hooksPath, err := InstallCursorHook(binPath)
		if err != nil {
			return fmt.Errorf("cursor hook: %w", err)
		}
		if added {
			fmt.Printf("  ✓ Cursor hook installed at %s\n", hooksPath)
		} else {
			fmt.Printf("  • Cursor hook already present at %s\n", hooksPath)
		}
		s.CursorHookAdded = true
	} else {
		fmt.Println("  • Cursor not detected — skipping hooks.json hook")
	}

	if detected.Codex.Present {
		added, configPath, err := InstallCodexHook(binPath)
		if err != nil {
			return fmt.Errorf("codex hook: %w", err)
		}
		if added {
			fmt.Printf("  ✓ Codex hook installed at %s\n", configPath)
		} else {
			fmt.Printf("  • Codex hook already present at %s\n", configPath)
		}
		s.CodexHookAdded = true
	} else {
		fmt.Println("  • Codex not detected — skipping config.toml hook")
	}

	if detected.Copilot.Present {
		added, hookPath, err := InstallCopilotHook(binPath)
		if err != nil {
			return fmt.Errorf("copilot hook: %w", err)
		}
		if added {
			fmt.Printf("  ✓ Copilot hook installed at %s\n", hookPath)
		} else {
			fmt.Printf("  • Copilot hook already present at %s\n", hookPath)
		}
		s.CopilotHookAdded = true
	} else {
		fmt.Println("  • Copilot not detected — skipping blamely.json hook")
	}

	if detected.Gemini.Present {
		added, settingsPath, err := InstallGeminiHook(binPath)
		if err != nil {
			return fmt.Errorf("gemini hook: %w", err)
		}
		if added {
			fmt.Printf("  ✓ Gemini hook installed at %s\n", settingsPath)
		} else {
			fmt.Printf("  • Gemini hook already present at %s\n", settingsPath)
		}
		s.GeminiHookAdded = true
	} else {
		fmt.Println("  • Gemini CLI not detected — skipping settings.json hook")
	}

	// 2. Global git post-commit hook.
	prior, hadPrior, err := InstallGitHook(binPath)
	if err != nil {
		return fmt.Errorf("git hook: %w", err)
	}
	s.PriorCoreHooksPath = prior
	s.HadCoreHooksPath = hadPrior
	s.GitHookInstalled = true
	if hadPrior {
		fmt.Printf("  ✓ Global git post-commit hook installed (previous core.hooksPath %q stashed)\n", prior)
	} else {
		fmt.Println("  ✓ Global git post-commit hook installed (no previous core.hooksPath)")
	}

	// 3. Daemon agent. We invalidate any stale port file BEFORE registering
	// the agent so the post-install health check can't false-positive on the
	// previous daemon's port. Then we register/restart and wait for the new
	// daemon to come up.
	if portPath, perr := config.PortFile(); perr == nil {
		_ = os.Remove(portPath)
	}
	agentRef, err := InstallDaemonAgent(binPath)
	if err != nil {
		return fmt.Errorf("daemon agent: %w", err)
	}
	s.LaunchAgentInstalled = true
	fmt.Printf("  ✓ Daemon agent installed (%s)\n", agentRef)

	// Block until the daemon actually answers /health, so the user knows
	// hooks are being listened to before this command exits.
	if port, derr := daemon.WaitForReady(8 * time.Second); derr != nil {
		diagnoseDaemon(derr, agentRef)
	} else {
		fmt.Printf("  ✓ Daemon listening on 127.0.0.1:%d (ready to receive hooks)\n", port)
	}

	// 4. PATH entry in the user's shell rc so `blamely` is on PATH after
	// they reload their shell. Best-effort — if shell detection fails or the
	// rc isn't writable, we print a manual hint instead of failing the
	// install (the binary is still on disk and the daemon is still wired up).
	if rcPath, added, perr := InstallPathEntry(); perr != nil {
		fmt.Printf("  • Could not auto-add ~/.blamely/bin to PATH: %v\n", perr)
	} else {
		s.PathRcFile = rcPath
		if added {
			s.PathEntryAdded = true
			fmt.Printf("  ✓ PATH entry added to %s (reload your shell or run `source %s`)\n", rcPath, rcPath)
		} else {
			fmt.Printf("  • PATH entry already present in %s\n", rcPath)
		}
	}

	// 5. Default exclude file. We never overwrite an existing one — users
	// edit ~/.blamely/exclude to customise what's skipped from attribution,
	// and a fresh install must keep their edits intact.
	if excludePath, created, eerr := config.EnsureDefaultExcludeFile(); eerr != nil {
		fmt.Printf("  • Could not write default exclude file: %v\n", eerr)
	} else if created {
		fmt.Printf("  ✓ Default exclude list written to %s\n", excludePath)
	} else {
		fmt.Printf("  • Exclude list already present at %s\n", excludePath)
	}

	// 5b. Default config file. Like the exclude list, we never overwrite an
	// existing one — users edit ~/.blamely/config.json to choose what each
	// commit's git note includes (file detail, conversation, tokens, …).
	if configPath, created, cerr := config.EnsureDefaultConfigFile(); cerr != nil {
		fmt.Printf("  • Could not write default config file: %v\n", cerr)
	} else if created {
		fmt.Printf("  ✓ Default config written to %s\n", configPath)
	} else {
		fmt.Printf("  • Config already present at %s\n", configPath)
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

func printDetected(d *Detected) {
	fmt.Println("Detected AI tools:")
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
		mark := "absent"
		if row.p.Present {
			mark = "found"
		}
		hint := ""
		if h := row.p.FirstHint(); h != "" {
			extra := ""
			if more := len(row.p.Hints) - 1; more > 0 {
				extra = fmt.Sprintf(" (+%d more)", more)
			}
			hint = "  (" + h + extra + ")"
		}
		fmt.Printf("  %-16s %s%s\n", row.name, mark, hint)
	}
	fmt.Println()
}

func printNextSteps(d *Detected) {
	fmt.Println("Next steps:")
	if d.Claude.Present {
		fmt.Println("  • Make an edit with Claude Code, then commit. Run `blamely report HEAD` to see the per-line attribution.")
	} else {
		fmt.Println("  • Claude Code wasn't detected. Install it, run `blamely install` again, or add the hook manually.")
	}
	if !d.Cursor.Present && !d.Codex.Present && !d.Copilot.Present && !d.Gemini.Present {
		fmt.Println("  • Cursor/Codex/Copilot/Gemini integrations will activate automatically once those tools appear.")
	}
	fmt.Println("  • `blamely status` shows the daemon health.")
	fmt.Println("  • `blamely uninstall` reverses every change above.")
}

