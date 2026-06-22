# Attribution v2 — IDE verification checklist

Goal: confirm the three things only a running IDE/commit can show — (a) the **gutter**
paints from the v2 working log, (b) the **working-log files** are written, (c) the
commit **note flips** and the **dual-run divergence** reports v2 correcting v1.

Everything here is **flag-gated**; with the flag off, nothing changes.

## Artifacts (built from the `attribution-v2` branches)
- CLI binary: `/tmp/blamely-v2-verify/bin/blamely`
- VS Code VSIX: `vscode-plugin/blamely-1.6.3.vsix`
- IntelliJ plugin: `intellij-plugin/build/distributions/Blamely-intellij-1.6.3.zip`

---

## 0. Install the v2 CLI binary (so the gutter query + commit flip use v2 code)
The plugin gutter calls `~/.blamely/bin/blamely authorship`, and the global
post-commit hook calls `~/.blamely/bin/blamely attribute` — both must be the v2 build.

```sh
cp ~/.blamely/bin/blamely ~/.blamely/bin/blamely.bak        # back up the current one
cp /tmp/blamely-v2-verify/bin/blamely ~/.blamely/bin/blamely # install the v2 build
~/.blamely/bin/blamely authorship --help                     # sanity: command exists
```
Restore afterwards with: `cp ~/.blamely/bin/blamely.bak ~/.blamely/bin/blamely`.
(The running daemon keeps the old inode; v2 paths don't use the daemon, so no restart
needed. Optionally `blamely daemon` restart for full consistency.)

## 1. Install the plugin
- **VS Code:** Command Palette → "Extensions: Install from VSIX…" → pick
  `blamely-1.6.3.vsix`. Reload.
- **IntelliJ:** Settings → Plugins → ⚙ → "Install Plugin from Disk…" → pick
  `Blamely-intellij-1.6.3.zip`. Restart.

## 2. Turn on the flag
- **VS Code:** Settings → search `blamely.attributionV2` → enable (or add
  `"blamely.attributionV2": true` to settings.json).
- **IntelliJ:** Settings → Tools → Blamely → enable Attribution v2 (the
  `attributionV2` setting). *(If there's no UI control yet, set it once via the IDE's
  registry/settings; the backing flag is `BlamelySettings.attributionV2`.)*
- **For commits** (the flip runs in the post-commit hook): commit from a **terminal**
  with the env set, so the hook inherits it:
  ```sh
  export BLAMELY_ATTRIBUTION_V2=1
  ```

## 3. The test scenario (in a throwaway repo)
```sh
export BLAMELY_ATTRIBUTION_V2=1
mkdir /tmp/v2demo && cd /tmp/v2demo && git init && git checkout -b main
printf 'human one\nhuman two\n' > app.py
git add . && git commit -m c1
```
Open `/tmp/v2demo/app.py` in the IDE. Now reproduce the classic failure: **type two
lines yourself, then let an AI tool add lines below.**

## 4. What to check
**(a) Gutter** — the human-typed lines show the **Human** icon, the AI-added lines
show the **AI** icon. The bug v2 fixes is "type-then-AI shows everything as AI" — it
should NOT happen now.

**(b) Working log written:**
```sh
find /tmp/v2demo/.git/blamely/working_logs -name '*.json' -exec cat {} \;
# expect lines 1-2 author "human", AI lines author "ai" with a tool
```

**(c) Gutter source directly (what the overlay reads):**
```sh
BLAMELY_ATTRIBUTION_V2=1 ~/.blamely/bin/blamely authorship /tmp/v2demo/app.py
# JSON: human ranges + ai ranges, matching the gutter
```

**(d) Commit flip + dual-run** — commit the change, then run attribute manually to
see the divergence line and the note:
```sh
git add . && git commit -m c2
SHA=$(git rev-parse HEAD)
BLAMELY_ATTRIBUTION_V2=1 ~/.blamely/bin/blamely attribute /tmp/v2demo "$SHA"
#   stderr: attribution-v2 dual-run … divergent=N (v1_ai=… v2_ai=…)  ← v2 should be the correct one
git notes --ref=refs/notes/blamely show "$SHA"
#   the AI lines carry "author_type":"AI","tool":"…"; human lines are Human
```

## 5. Success criteria (this *is* the start of the §12 soak)
- Gutter: human lines Human, AI lines AI (no type-then-AI mislabel). ✅
- Working-log files present and correct. ✅
- Note: AI lines attributed AI/tool; human lines Human. ✅
- Dual-run line: every `divergent` case is **v2 being right** (e.g. `v1_ai=0 v2_ai=1`
  where v1 wrongly said Human). Record these over a window — that's the §12 gate that
  unblocks Phase 6.

## Notes / known limits while experimental
- The gutter overlay covers the **active editor**; the status bar / sidebar still
  aggregate the v1 data. A v1 timer refresh may transiently repaint between overlay
  ticks (edit/save/switch re-asserts within ~300 ms).
- Commit the test from a terminal with `BLAMELY_ATTRIBUTION_V2=1` exported (IDE git
  UIs may not pass the env to the hook).
