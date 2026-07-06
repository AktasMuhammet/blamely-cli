# Changelog

Notable changes to the **Blamely CLI** follow [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). This project uses [semantic versioning](https://semver.org/).

## [Unreleased]

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
