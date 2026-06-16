# Changelog

Notable changes to the **Blamely CLI** follow [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). This project uses [semantic versioning](https://semver.org/).

## [Unreleased]

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
