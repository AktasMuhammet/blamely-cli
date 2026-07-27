# Changelog

Notable changes to the **Blamely CLI** follow [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). This project uses [semantic versioning](https://semver.org/).

## [Unreleased]

## [1.6.9] - 2026-07-27

### Fixed

- **Blamely's background service now starts reliably on macOS.** Some users — especially on company-managed Macs or after a reinstall — ended up with everything installed but the service never running, so nothing was tracked. Install now starts the service in a way macOS respects in all of these cases, including when it had been switched off by an earlier uninstall.
- **Company-wide installs no longer look broken.** When IT pushes Blamely to a Mac while the user isn't logged in, install used to end with a scary "daemon health check failed" warning even though nothing was wrong. It now simply says the service will start automatically at the user's next login. A step-by-step guide for IT teams deploying via Jamf/Kandji/Intune is included (`docs/macos-bulk-deployment.md`).
- **Updating Blamely on Windows now switches to the new version immediately.** Previously the old version could quietly keep running after an update — and stop receiving edits from your AI tools — until you signed out or restarted.
- **Fewer false alarms on slower machines.** On corporate Windows machines where antivirus scans new programs, the service can take a little longer to start the first time. Install now waits long enough for that instead of reporting a failure while the service was actually coming up fine.
- **Editor extension installs are reported correctly.** In some editors (seen with Antigravity on Windows), the Blamely extension installed successfully but was reported as failed. Install now reports what actually happened.
- **Better guidance when something really is wrong.** If the service genuinely fails to start on macOS, install now tells you the specific cause and the exact command to fix it, instead of a generic list of things to try.

## [1.6.8] - 2026-07-13

### Added

- **Attribution now survives cherry-pick, squash, and stash.** When you cherry-pick a commit, squash commits together, or stash and re-apply your work, Blamely keeps the AI-vs-human credit on each line instead of losing it. This joins the existing support for rebase and amend, so reorganizing your history no longer wipes out who wrote what.

### Fixed

- **Blamely keeps running on a laptop that's on battery (Windows).** Previously, if you started your laptop while unplugged, Windows quietly refused to launch Blamely's background service until you plugged in — so nothing was tracked in the meantime. It now starts and stays running whether or not you're on battery.
- **RustRover now gets the Blamely plugin.** If you use RustRover, the installer was skipping it and leaving it without the plugin. It's now detected like every other JetBrains IDE, so the plugin installs automatically.
- **More reliable attribution on Windows across editors.** Fixed cases where Windows file paths (with drive letters like `C:\`) weren't matched correctly in Cursor, Antigravity, and Claude, which could leave AI edits credited to the wrong person.
- **More accurate per-file AI vs. human line counts.** The breakdown of how many lines were written by AI versus a human on each file is now calculated more reliably, so totals no longer drop lines in edge cases.

## [1.6.7] - 2026-07-06

### Added

- **Files deleted by the Cursor agent are now credited to AI.** When you ask Cursor to delete a file, the deletion is attributed to Cursor instead of showing up as a human change.
- **Better Windows support for AI deletions.** File deletions made through the terminal on Windows are now recognized and attributed correctly.
- **Support for custom tool locations.** If your team runs Codex or Claude from a non-standard location, you can now point Blamely at it so those edits are still tracked. Set `CODEX_HOME` and/or `CLAUDE_CONFIG_DIR` before running `blamely install` (Blamely picks them up automatically), or add them anytime with `blamely config add tools.codex_home <path>` / `blamely config add tools.claude_config_dir <path>`. These are additive — the standard `~/.codex` and `~/.claude` locations keep working as before.
- **One-click installers.** Blamely now ships proper installers for Windows, macOS, and Linux, so you no longer need the command line to get set up.
- **Branded Windows installer.** The Windows installer and the installed app now show the Blamely icon instead of a blank one.

### Fixed

- **More accurate attribution when AI rewrites a whole file.** Both the new and replaced lines are now credited correctly.
- **Windows path matching.** Deletions on Windows are matched reliably instead of occasionally falling back to a human change.
- **File deletions from Copilot and Cursor agents** are now recognized and credited to the right tool.
- **Cleaner install log.** The setup log no longer shows garbled characters.
- **Reports stay fully private.** HTML reports now open completely offline — they no longer reach out to Google for fonts, so nothing leaves your machine when you open one.

##DASDA [1.6.0] - 2026-06-15

### Added

- **Claude Desktop attribution** — a new watcher records file edits and deletions made by Claude Desktop's "cowork" sandbox, which (unlike Claude Code) exposes no PostToolUse hook. It tails the app's undocumented logs, maps the sandbox VM's mounted repo path back to the host repo, and records deletions straight from the `rm` command (hashed from HEAD content) so attribution doesn't depend on when the VM syncs its changes back to the host filesystem.

### Changed

- **VS Code extension installed from Open VSX, not the Microsoft Marketplace** — `blamely install` now downloads the extension `.vsix` from [Open VSX](https://open-vsx.org/extension/blamely/blamely) and installs it by path (`--install-extension <path>`). Installing by path is registry-independent, so it works on VS Code proper — whose Marketplace listing was delisted — as well as the Open-VSX-based forks (Cursor, Antigravity). The `.vsix` is downloaded once per run and reused for every detected editor.

### Fixed

- **`blamely report --html` crashed on obfuscated release builds** — `garble -literals -tiny` renamed the HTML report's view-model fields, so the template's reflection-based `{{.ShortHash}}` lookups failed at run time (`can't evaluate field ShortHash`) only in published binaries. The report's root view-model type is now marked as reflection-used (`reflect.TypeOf`), so garble preserves its (and the nested structs') field names. Verified by rebuilding with the pinned garble/Go release toolchain.
- **Copilot chat attribution** — `scanTextEdits` now scans Copilot chat sessions incrementally (much better performance on long-running sessions), detects new request appends mid-session, and handles responses more robustly — fixing cases where Copilot chat edits failed to load and were left unattributed.
- **`blamely install` no longer fails on VS Code** — the previous `--install-extension Blamely.blamely` resolved against the Microsoft Marketplace, which no longer lists the extension, so the install silently did nothing on VS Code proper. When the Open VSX download is unavailable (offline / registry down), it falls back to the registry id, which still resolves via the editor's own gallery on Cursor/Antigravity.

### Notes

- **Gemini (Antigravity)** — the bundled Gemini *agent* is attributed via its transcript logs (shipped earlier). The Gemini *inline-completion* fix this cycle is editor-plugin side — see the VS Code extension's 1.6.0 changelog (Antigravity inline completions now attributed as AI).
