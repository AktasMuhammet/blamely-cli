# Blamely

**Know who wrote what.** Blamely traces your code changes and attributes them to AI tools or humans — line by line — then writes the result as a git note on every commit.

After you commit, you get an AI vs Human bar right in your terminal:

```
AI 59% (20)  [████████████████████░░░░░░░░░░░░░░░░░░░░]  Human 41% (14)
```

All data stays on your machine. No cloud, no telemetry.

## Supported tools

| Tool | Detection | Attribution |
|------|-----------|-------------|
| [Claude Code](https://docs.anthropic.com/en/docs/claude-code) | ✓ | chat |
| [Cursor](https://cursor.com) | ✓ | chat, agent |
| [Codex CLI](https://github.com/openai/codex) | ✓ | cli |
| [GitHub Copilot](https://github.com/features/copilot) | ✓ | completion |
| [Gemini CLI](https://github.com/google-gemini/gemini-cli) | ✓ | cli |

Blamely auto-detects which tools are installed and wires up hooks only for those. If you install a new tool later, run `blamely install` again — it's idempotent.

## Quick start

### Install from a release

Download the archive for your platform from [GitHub Releases](https://github.com/blamely/blamely/releases), extract it, and put `blamely` on your `PATH`.

```bash
# macOS / Linux example (adjust arch and version as needed)
curl -LO https://github.com/blamely/blamely/releases/download/v0.1.0/blamely_v0.1.0_darwin_arm64.tar.gz
tar xzf blamely_v0.1.0_darwin_arm64.tar.gz
sudo mv blamely_v0.1.0_darwin_arm64/blamely /usr/local/bin/
```

Then run:

```bash
blamely install
```

Reload your shell (or `source ~/.zshrc` / `~/.bashrc`) so the PATH entry takes effect.

### Install from source

Requires [Go 1.26+](https://go.dev/dl/) and Git.

```bash
git clone https://github.com/blamely/blamely.git
cd blamely
./scripts/install.sh          # macOS / Linux
# pwsh -File scripts\install.ps1   # Windows
```

The install script builds the binary, runs `blamely install`, and cleans up any stale legacy hooks.

### Verify

```bash
blamely status    # daemon health + detected tools
blamely doctor    # full self-check (hooks, PATH, binary, DB)
```

Make an edit with your AI tool, commit, and you should see the attribution bar. Then:

```bash
blamely report HEAD    # line-by-line breakdown
blamely stats          # deep single-commit view
```

## How it works

```
 AI tool hook                Global git hook
 (PostToolUse)               (post-commit)
      │                            │
      ▼                            ▼
 blamely record <tool> ──►  blamely daemon  ◄── filesystem / log watchers
      │                     (127.0.0.1)           (Codex, Cursor, Copilot…)
      │                            │
      └──────── SQLite ────────────┘
                    │
                    ▼
           blamely attribute
                    │
                    ▼
         refs/notes/blamely  (per-commit JSON note)
                    │
                    ▼
              AI / Human bar
```

1. **Record** — When an AI tool edits a file, its hook calls `blamely record <tool>`, which sends the edit to the local daemon.
2. **Watch** — The daemon also tails Codex sessions, Cursor logs, and Copilot transcripts to catch edits that don't go through hooks.
3. **Attribute** — On every commit, a global `post-commit` hook runs `blamely attribute`, which diffs the commit against stored edits and writes a per-line attribution note.
4. **Report** — Use `blamely report`, `blamely stats`, or `blamely history` to inspect the results.

Attribution data lives in two places, both local:

- **Git notes** at `refs/notes/blamely` — travels with the repo, shareable via `git push` (notes are not pushed by default; use `git push origin refs/notes/blamely` if you want them on the remote).
- **SQLite database** at `~/.blamely/db.sqlite` — raw edit events used to compute attribution.

## Commands

| Command | Description |
|---------|-------------|
| `blamely install` | Detect AI tools, install hooks, register daemon, add PATH entry |
| `blamely uninstall` | Reverse everything `install` did (keeps attribution history) |
| `blamely status` | Daemon health, detected tools, recent activity |
| `blamely doctor` | Read-only self-check with fix recommendations |
| `blamely detect` | List which AI tools were found (read-only) |
| `blamely repair` | Remove stale per-repo hooks from older installations |
| `blamely report [<sha>]` | Line-by-line attribution for a commit |
| `blamely stats [<sha>]` | Deep view: files, tools, tokens, coding time (default: `HEAD`) |
| `blamely history` | Aggregate report across noted commits |
| `blamely history --since 7d` | Limit to a time window (`7d`, `30d`, `90d`, `1y`) |
| `blamely history --all` | Include all tracked repos, not just the current one |
| `blamely --version` | Print version |

Set `NO_COLOR=1` to disable ANSI colors in terminal output.

## What gets installed

`blamely install` makes these changes (all reversible with `blamely uninstall`):

| Change | Location |
|--------|----------|
| Stable binary copy | `~/.blamely/bin/blamely` |
| Daemon agent | launchd (macOS) / systemd user unit (Linux) / Scheduled Task (Windows) |
| Global git hook | `git config --global core.hooksPath` → `~/.blamely/git-hooks/` |
| Claude Code hook | `~/.claude/settings.json` |
| Cursor hook | `~/.cursor/hooks.json` |
| Codex hook | `~/.codex/config.toml` |
| Copilot hook | `~/.copilot/hooks/blamely.json` |
| Gemini hook | `~/.gemini/settings.json` |
| PATH entry | Appended to `~/.zshrc`, `~/.bashrc`, or equivalent |

Existing hooks from other tools are preserved — Blamely merges into your settings rather than replacing them.

## Example output

**Commit bar** (printed automatically after `git commit`):

```
AI 72% (18)  [████████████████████████████░░░░░░░░░░░░]  Human 28% (7)
  claude 12 (claude-opus-4-6) — 4200 in / 890 out tok
  cursor 6 (composer-1) — 1100 in / 340 out tok
```

**`blamely stats HEAD`**:

```
commit a1b2c3d4e5f6  "Add user authentication"  (2h ago)
  author: you@example.com
  branch: main

Changes:
  +25 added  (AI 18 · human 7)
  ─────────
  25    net

AI attribution:
  claude       12 lines  chat          claude-opus-4-6  in=4.2k out=890 cache=1.1k
  cursor        6 lines  chat          composer-1       in=1.1k out=340 cache=0

Coding time:  ~47 min  (first edit → commit)
```

**`blamely history --since 30d`**:

```
Blamely history · /path/to/repo  (last 720h0m0s · 42 commits)

Changes:
  +1847 added   (AI 1203 · human 644)
  -312  deleted
  ────────────
  1535  net

By tool:
  claude       890  ████████████████░░░░  48.2%
  cursor       213  ████░░░░░░░░░░░░░░░░  11.5%
  human        644  █████████████░░░░░░░  34.9%
  ...
```

## Troubleshooting

**Daemon not running**

```bash
blamely doctor
blamely install    # re-register the daemon agent
```

Check `~/.blamely/daemon.log` for errors.

**No attribution bar after commit**

- Confirm the daemon is up: `blamely status`
- Confirm hooks are wired: `blamely doctor`
- Make sure you edited files *before* committing (Blamely attributes edits it observed in the lookback window, default 8 hours).

**Upgrading from an older per-repo hook setup**

```bash
blamely repair     # removes stale .git/hooks/post-commit files
blamely install    # ensures the global hook is active
```

**PATH not updated**

Restart your terminal or run `source ~/.zshrc`. The binary is always available at `~/.blamely/bin/blamely`.

## Uninstall

```bash
blamely uninstall
```

This removes hooks, the daemon agent, the PATH entry, and the stable binary. Your attribution history at `~/.blamely/db.sqlite` and any git notes already written are kept. To wipe everything:

```bash
rm -rf ~/.blamely
```

## Requirements

- **Git** 2.x+
- **macOS**, **Linux**, or **Windows**
- At least one supported AI coding tool (recommended, but not required — human-only edits are tracked too)

## Privacy

Blamely runs entirely on your machine. Edit events are stored in a local SQLite database. Attribution is written to local git notes. Nothing is sent to Blamely or any third party.

## Contributing

Issues and pull requests are welcome. For development:

```bash
go test ./...
./scripts/install.sh rebuild    # rebuild binary without touching install state
```
