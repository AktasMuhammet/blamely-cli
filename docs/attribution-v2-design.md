# Attribution v2 — Design

**Status:** Implemented behind a flag (engine + CLI pipeline complete; editor display + git-op lifecycle in progress) · **Owner:** Blamely · **Scope:** blamely-cli + VS Code plugin + IntelliJ plugin

> **Project constraints (apply to all of v2):**
> - **Naming:** do not reference any external project by name in code, comments, or docs — describe approaches generically.
> - **Cross-platform:** every change must work on **Windows, Linux, and macOS**.

This document specifies a migration of Blamely's line-level AI/Human attribution from the
current **"record per-line content hashes, then guess at commit"** model to a
**"capture a pre-edit baseline, diff it, and maintain a stateful working log"** model.

Implementation is **on by default** as of this branch — `BLAMELY_ATTRIBUTION_V2`
(CLI/daemon env) and `blamely.attributionV2` (both editor plugins) default to ON, with
opt-out (env `0/false/off`; setting `false`). It **degrades safely**: any file with no
working log falls back to the legacy engine, so default behavior only changes where v2
actually captured. See **§16 — Implementation status** for what is built today;
correctness is verified against the **invariants (§4)** and the **proof matrix (§11)**.

---

## 1. Problem statement

Today, each tool records the lines it wrote as per-line `content_sha` rows (SQLite), and at
**commit time** the daemon re-matches those hashes onto the `git diff` with budgets/drift
heuristics. This is fundamentally a **guess**, and it fails in recurring, reproducible ways:

| Symptom (observed in the field) | Root cause |
|---|---|
| Same content on different lines → gutter "sometimes right, sometimes wrong" | Content-hash can't disambiguate duplicate lines; budget/drift picks the wrong occurrence |
| User types lines, then AI edits → user's lines shown as AI | The only baseline is a **stale post-edit cache**; it misses human typing between recorded edits |
| Whole-file rewrite re-includes unchanged/human lines → all AI | Re-emitted lines look "new" to a hash matcher; patched per-tool, never solved at the root |
| File with a space in the name → 100% Human | Commit-time diff-path parsing (`+++ b/login page.html\t`) — a hash/path-matching artifact |
| "Note correct but gutter wrong" (and vice-versa) | Note and gutter derive from **different** code paths |
| Daemon down / socket / Windows `APPDATA` → 100% Human | Correctness depends on a running daemon + DB |

These are not independent bugs; they are all symptoms of **guessing authorship after the fact
by content hash**. The fix is to stop guessing.

The thesis adopted here: **"Detecting AI code is an anti-pattern."** Authorship must come
from an *observed* edit, never from a retroactive hash match.

---

## 2. Goals

- **G1** — Determine each line's author from an **observed edit** (a diff of before→after, or a
  precise editor event), never from content-hash matching.
- **G2** — **One** attribution engine shared by all tools and both editors (no per-tool narrowing).
- **G3** — The **committed note and the live gutter** derive from the **same** source.
- **G4** — Correctness does **not** depend on a running daemon or a database.
- **G5** — Reuse the existing `record` hook and live plugin signal; minimize new surface.

## 2.1 Non-goals (honest scope)

- **N1** — Cloud / background agents that don't run local hooks → **uncovered** (attributed Human).
- **N2** — Tools with neither a hook/plugin signal nor a watcher → **degraded** (Human).
- **N3** — **Sub-line** (character-range) attribution → deferred; v2 is line-level (see Decision A).
- **N4** — Cross-commit reporting performance is a *later* optimization (see §3.4), not a v2 blocker.

---

## 3. Target architecture

### 3.1 The shift
- **From:** record hashes → SQLite → re-match onto `git diff` at commit.
- **To:** maintain a **working log** of per-line authorship, updated by **diffing a pre-edit
  baseline against post-edit content**, flushed to a **git note** at commit.

### 3.2 Where data lives (no database)
| Data | Home | Lifetime |
|---|---|---|
| **Committed authorship** | `git notes` (`refs/notes/blamely`, schema-compatible) | permanent, syncable |
| **Uncommitted authorship (working log)** | `.git/blamely/working_logs/<branch>/<base_sha>/<path>.json` | until commit/rotate |
| **Pre-edit baselines** | `.git/blamely/working_logs/<branch>/<base_sha>/.baselines/<path>` | consume-once |
| **Live editor state** | in-memory in the plugin (flushed to working-log files) | session |
| **Watcher resume state** | small state file per watcher | machine-local |

**SQLite is removed** as the source of truth. It may return *only* as an optional, rebuildable
**reporting cache** (§3.4), never authoritative. This is what gives us **G4** and kills the
"daemon down → 100% Human" failure class.

### 3.3 Capture model — reuse `record`, no new `checkpoint` verb
The baseline is "the working log's current known content," kept fresh by whoever sees the edit:

- **In-editor edits (primary):** the plugin is a **live tracker** — every change updates the
  working log immediately:
  - human typing/paste → lines marked **Human** (this is what fixes "type-then-AI");
  - inline completion accept → exact inserted range marked **AI** (`gen_type=completion`) —
    no diff needed, the editor reports the exact range;
  - chat/agent apply → plugin diffs its pre-apply baseline → **AI** lines.
- **Terminal/CLI agents:** `blamely record <tool>` on `PostToolUse` diffs new content against the
  working log. If the working log is stale for that file (the editor never saw it), fall back to
  **`record --pre`** on `PreToolUse` (a *mode of `record`*, not a new command) to snapshot disk
  just before the agent writes; last resort = diff against `HEAD`.

> **Why no `checkpoint` verb:** an explicit pre/post checkpoint is only needed without a live
> editor tracker. Blamely's plugin already observes every keystroke, so the pre-edit baseline is
> just "the live working log." `record` (post) + the live tracker covers the editor workflows;
> `record --pre` is a fallback for headless/terminal flows only.

### 3.4 Reporting
`blamely report` reads git notes directly. If aggregation over very large
histories is too slow, add a **rebuildable** SQLite cache derived from notes — explicitly *not* a
source of truth, droppable at any time. Out of scope for v2.

---

## 4. Correctness contract (invariants)

Every test in §11 maps to one of these. The implementation is "correct" iff these always hold.

- **I1 — No guessing.** A line's author comes only from an *observed* edit (baseline diff,
  completion accept, keystroke). Never from content-hash matching.
- **I2 — Human content is never AI.** Even when byte-identical to AI content elsewhere.
- **I3 — Re-emit is inert.** An AI edit that re-includes unchanged lines does not change their
  authorship.
- **I4 — One source of truth.** The committed note and the live gutter derive from the same
  working log.
- **I5 — Honest fallback.** An unobserved edit defaults to **Human**, never guessed-AI.
- **I6 — Stable under motion.** Attribution survives line shift / move / reflow per the documented
  transform rules (§8).

---

## 5. Key design decisions (resolved)

| # | Decision | Resolution | Rationale |
|---|---|---|---|
| **A** | Char-level vs line-level | **Line-level for v2**; reserve a `char_ranges` field in the format for later | Matches the gutter; char-level is a large add and can layer on without a format break |
| **B** | Baseline source of truth | **Live working log (primary) → `record --pre` (fallback) → HEAD (last resort)**; baseline is always **on-disk content**, and the plugin **flushes buffer→log on focus-loss** before yielding to terminal agents | Resolves the editor-buffer-vs-disk race; covers the type-then-AI bug without a checkpoint |
| **C** | Working-log lifecycle | Keyed by `repo+branch+base_sha+file`; **flush+rotate on commit**; git-op hooks migrate it (Phase 5); directory key makes checkout/branch-switch safe | Prevents stale attributions leaking across commits/branches |
| **D** | Two-writer concurrency (plugin + CLI) | **Atomic write (temp+rename) + per-file lock**; the diff/transform is the only mutation and runs under the lock | Race-free read-modify-write with two independent writers |
| **E** | Force-split scope (AI re-credit) | Force-split applies **only within the diff's changed region**, never to context/unchanged lines | Too broad recreates the over-attribution bug; bounded by the diff it's safe |
| **F** | Completion vs typing | Keep deterministic editor signals (inline-suggest command/keybinding; IntelliJ `AnActionListener`); **on ambiguity default Human** (I5) | The one remaining judgment call; bias to not-AI |
| **G** | Backward compatibility | New engine writes the **same note shape**; `blame`/report read existing notes unchanged; **no history re-derivation** | Old commits keep working; migration is forward-only |

---

## 6. Data model & format (shared contract)

The working-log format is a **shared schema** implemented in **Go (CLI), TypeScript (VS Code),
Kotlin (IntelliJ)** — like today's daemon protocol. A single spec + shared **golden test vectors**
prevent the three implementations from drifting.

```jsonc
// .git/blamely/working_logs/<branch>/<base_sha>/<path>.json
{
  "schema": "blamely/working-log/1",
  "file": "src/login page.html",     // repo-relative; NEVER parsed from a diff header
  "base_sha": "…",                   // commit the baseline is relative to
  "blob_sha": "…",                   // sha256 of current file content (staleness check)
  "updated_ms": 0,                   // stamped by the writer (CLI/plugin)
  "lines": [
    { "start": 1, "end": 2,  "author": "human" },
    { "start": 3, "end": 7,  "author": "ai", "tool": "claude", "model": "claude-opus-4-8",
      "gen_type": "chat", "session": "…", "overrode": null },
    // reserved for Decision A (line-level v2 leaves this absent):
    // "char_ranges": [...]
  ]
}
```

Notes (committed) keep the **existing schema-2 shape** (Decision G), populated from the working log
instead of from hash-matching.

---

## 7. The single diff engine

```
Attribute(prior *WorkingLog, baseline, newContent string, author Author, nowMS int64) → *WorkingLog
```
- One function, used by every path (CLI `record`, plugin apply, watcher). Implemented three times
  from one spec — Go (`internal/authorship/attribute.go`), TypeScript, Kotlin — and pinned by the
  shared golden vectors (§6, §11).
- A line **unchanged** vs the baseline (an LCS match) keeps its **prior** author (so an AI edit that
  re-emits a human line stays Human, I3); an added/changed line is attributed to `author`; an
  uncovered line defaults to Human (I5).
- **Add** (empty baseline → all `author`), **edit** (diff), **delete** (lines vanish), **duplicate**
  (resolved by diff position, not hash), **reflow**, **move**, and **override** all fall out of the
  same call (transform rules in §8).
- **No content-hash guessing.** This replaces and retires the old hash budget/drift matcher and
  every per-tool narrowing branch (removed in Phase 6).

---

## 8. Edit-transform rules (correctness on hard cases) — **implemented**

All of the following are implemented identically in Go/TS/Kotlin and covered by named golden
vectors (§11):

- **Line shift** (insert/delete above) → ranges move, authorship preserved. *(LCS positional match.)*
- **Reflow / whitespace-only** → authorship inherited. Implemented as a **whitespace-normalized**
  line comparison in the LCS (`normalizeLineForMatch` = trim + collapse internal whitespace runs):
  a reindented/reformatted line is treated as unchanged and keeps its prior author. A substantive
  content change still mismatches → the editor. *(Token-level reflow, e.g. operator spacing, is a
  later refinement.)*
- **Move** → a relocated line carries its prior author. Implemented as **FIFO-by-content pairing**
  (`detectMoves`): each unmatched new line pairs with the first unmatched old line of identical
  normalized content. A block an AI moves stays Human; an AI block a human moves stays AI. *(This
  is line-granular, not a ≥3-line block heuristic; it is deterministic and parity-tested.)*
- **Human overwrites AI** → new line is the editor; `overrode` records the replaced author when its
  type differs. Implemented as `detectOverrode` (gap-positional replace pairing); a moved line is
  never treated as an override.
- **Duplicate identical lines** → resolved by *position in the diff*, not by hash — the unchanged
  occurrence matches in place; an extra copy is an add.

---

## 9. Component responsibilities

| Component | Role in v2 |
|---|---|
| **VS Code / IntelliJ plugin** | **Live tracker**: maintains the working log from real-time edits (typing=Human, completion/apply=AI); reads the working log for the **gutter**. Flushes on save/idle/focus-loss/commit. |
| **CLI (`blamely`)** | `record` (post) / `record --pre` (fallback) for terminal agents → diff engine → working log. **Commit hook**: working log → git note. **No daemon/DB needed for this path.** |
| **Daemon (slim, optional)** | Only the **watchers** for non-integrating tools (Copilot transcript, Codex/Cursor sessions, antigravity) that have no hook/plugin signal. File-backed watermarks. No DB. |
| **SQLite** | **Removed** as truth. Optional rebuildable reporting cache only (§3.4). |

---

## 10. Migration phases (incremental, dual-run, reversible)

Every phase is behind a feature flag and leaves `main` shippable. Status as built (✅ done,
◐ partial, ☐ remaining):

- **Phase 0 — Foundations (no behavior change). ✅** Working-log format spec + shared golden
  vectors; the `Attribute()` engine; atomic file read-modify-write + per-file lock in Go/TS/Kotlin.
- **Phase 1 — Capture. ✅** Editor live tracker in both IDEs (typing=Human, completion/apply=AI →
  working log); CLI `record` + **`record --pre`** → working log for terminal agents. Baselines per
  Decision B.
- **Phase 2 — Dual-run validation. ✅** Old hash engine kept; at commit the working log is compared
  to the v1 note and divergences are logged (`logV2Divergence`). No output change.
- **Phase 3 — Flip note to the working log. ◐** The **commit note** is rewritten from the working
  log when the flag is on (`flipNoteToWorkingLog`, recomputes totals/by_tool/by_gen_type). The
  **gutter** flip (plugins read the working log live) is the remaining half (needs a running IDE).
- **Phase 4 — Transform robustness. ✅** Reflow (whitespace-normalized matching), move detection
  (FIFO-by-content), and `overrode` (replace pairing) — all three languages, all parity-tested
  (§8). *(Token-aligned diff for token-level reflow is a later refinement.)*
- **Phase 5 — Git-op lifecycle. ◐** Done for the CLI path: **GC** of working logs whose base
  object git no longer has (`GCWorkingLogs`, safe by object-existence); **note-seeding** (carry
  committed authorship across a commit via `git blame` + notes, wired through `authorship.SeedHook`);
  and **history-rewrite robustness** — amend/rebase/cherry-pick add the blamely ref to
  `notes.rewriteRef` so git copies the (flipped) note onto the rewritten commit, and attribution
  **skips while a rewrite is in progress** (`InProgressOp`) so the per-commit hook doesn't clobber
  that copy. Verified end-to-end with a real `git rebase`. Merge-conflict-resolution attribution
  and the editor-path seed remain.
- **Phase 6 — Retire old engine. ☐** Gated on §12. Remove hash budget/drift + per-tool narrowing
  once the new engine has soaked clean; keep `content_sha` only as a degraded unobserved-edit
  fallback.

---

## 11. Proof obligations (test matrix)

A golden corpus exercised on **every cell** (mirrors a comprehensive tracker corpus plus our field
repros), with **shared vectors** across Go/TS/Kotlin:

- **operations** × `{add, edit, delete, move, reflow, rename}`
- **authors** × `{human, AI, human-then-AI, AI-then-human-override}`
- **edge cases** × `{duplicate identical lines, space-in-filename, CRLF, empty file, unicode, large file}`
- **tools** × `{claude, codex, cursor, copilot, gemini, inline-completion}`
- **git-ops** × `{commit, amend, rebase, squash, stash/pop, cherry-pick, revert}`

Each test asserts a specific invariant (I1–I6). Field repros included as named fixtures:
`type-then-ai`, `whole-file-rewrite`, `duplicate-lines-drift`, `space-in-filename`, `crlf`.

---

## 12. Validation gate (measurable "flawless")

Phase 3 flips **only when**:
1. **Divergence = 0** between old and new engines on the golden corpus, and
2. Every divergence on a panel of real repos is **explained** (new engine correct), and
3. The new engine has run **dual** in production for the defined soak window.

Rollback at any point = flip the feature flag. Nothing in Phase 6 is deleted until the gate has
held through the soak window.

---

## 13. Risk register

| Risk | Mitigation |
|---|---|
| Three implementations (Go/TS/Kotlin) drift | One format spec + **shared golden vectors**; CI runs the vectors in all three |
| Editor-buffer vs disk baseline race | Decision B: disk is truth; plugin flushes on focus-loss before terminal agents run |
| High-frequency completion writes thrash disk | In-memory working log; debounced/batched flush (save/idle/focus/commit) |
| Two-writer file races | Decision D: atomic write + per-file lock |
| Terminal-only edits to files the editor never saw | `record --pre` fallback; else degraded HEAD baseline (documented, honest) |
| Big-bang regression | Dual-run gate (§12) + feature flags + forward-only compat (Decision G) |
| Loss of fast aggregate reporting | Optional rebuildable cache (§3.4), added only if needed |

---

## 14. Coverage & honest limits

- **Full coverage:** edits made in the editor (typing, completions, chat/agent applies) and
  terminal agents that run local hooks.
- **Degraded (HEAD baseline):** terminal edits to files the editor never saw and no `record --pre`.
- **Uncovered (→ Human):** cloud/background agents (N1) and tools with no signal at all (N2).

These limits are stated so "flawless" means *flawless within the covered set* — never silent
mis-attribution.

---

## 15. Open questions

1. ~~Working-log location~~ **Resolved:** `.git/blamely/working_logs/<branch>/<base_sha>/<path>.json`
   with baselines under `.baselines/` of the same dir (§3.2, implemented in `store.go`).
2. Watcher watermark file format/location (per-watcher file vs one state file) — unchanged from v1.
3. Soak-window length and the real-repo panel for the §12 gate — **still open** (the §12 gate has
   not yet been run; it is what unblocks the gutter flip + Phase 6).
4. Whether the slim daemon stays long-running or becomes launch-on-demand — unchanged from v1.

---

## 16. Implementation status (as built)

Flag: `BLAMELY_ATTRIBUTION_V2` (CLI/daemon env) and `blamely.attributionV2` (both editor
settings), **on by default**; opt out with the env set to `0/false/off/no/disable` or the
setting `false`. With v2 off (opt-out), behavior is the legacy engine, unchanged.

| Area | State | Where |
|---|---|---|
| Engine (`Attribute`) — add/edit/delete/dup/**reflow**/**move**/**override** | ✅ Go · TS · Kotlin | `internal/authorship/attribute.go` + plugin ports |
| Working-log store (atomic write + per-file lock) | ✅ Go · TS · Kotlin | `internal/authorship/store.go` + ports |
| Shared golden vectors (18) + cross-language sync guard | ✅ | `internal/authorship/testdata/golden_vectors.json` |
| Terminal capture (`record` for 5 tools) + **`record --pre`** baseline | ✅ | `internal/tools/*`, `cmd/blamely/main.go` |
| Editor capture (live tracker, typing=Human, completion/apply=AI) | ✅ both IDEs | `WorkingLogTracker` (TS/Kotlin) + completion hooks |
| Phase 2 dual-run divergence logging | ✅ | `internal/gitnotes/attribution_v2_dualrun.go` |
| Phase 3 **note** flip (note ← working log) | ✅ | `internal/gitnotes/attribution_v2_flip.go` |
| Phase 5 GC (prune dangling-base logs) | ✅ | `internal/authorship/gc.go` |
| Phase 5 note-seeding (committed authorship across commits) | ✅ CLI path | `internal/gitnotes/attribution_v2_seed.go` + `authorship.SeedHook` |
| Gutter source — `blamely authorship <file>` (single per-line source, I4) | ✅ | CLI command; seeds + returns the working log; smoke-verified |
| Editor-path note-seeding | ✅ | implicit in `authorship` (seeds committed authorship on query) |
| Phase 3 **gutter** rendering flip (plugins paint from `authorship`) | ◐ | `GutterV2` overlay implemented + compiling in BOTH IDEs (flag-gated, active editor); needs an IDE run to verify icons |
| History-rewrite robustness (amend/rebase/cherry-pick) | ✅ | notes.rewriteRef + skip-while-in-progress; real-rebase e2e |
| Merge-conflict-resolution attribution | ☐ | Phase 5 remainder |
| Phase 6 retire old engine | ☐ | gated on §12 |
| PreToolUse `--pre` install wiring | ☐ deferred | per-call hook cost while experimental |

**Architecture notes worth recording:**
- **Note-seeding uses a hook, not a call.** `gitnotes` already imports `internal/tools`, so the
  engine cannot import `gitnotes`. `authorship.SeedHook` is a function pointer the top-level `main`
  package registers (`SeedCommittedWorkingLog`), keeping the engine dependency-free.
- **GC is by object-existence, not reachability** — a log is pruned only once its base *object* is
  gone (post amend/rebase/`git gc`), so it can never describe content reachable from any ref.
- **Reflow ≠ token diff (yet)** and **move is line-granular FIFO**, not a ≥3-line block heuristic;
  both are deterministic so the three language ports stay byte-identical (the vectors enforce it).
- **The §12 gate has not run.** It is the explicit prerequisite for the gutter flip and Phase 6;
  until then those stay behind the flag and `main` is unaffected.

---

## Appendix A — Glossary

- **Working log** — per-file, uncommitted line-authorship state, transformed on each edit.
- **Baseline** — the file content immediately before an edit, used as the diff's old side.
- **Live tracker** — the editor plugin maintaining the working log in real time.
- **Dual-run** — old and new engines computed in parallel to measure divergence before flipping.
