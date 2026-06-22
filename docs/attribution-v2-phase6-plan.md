# Phase 6 — Retire the legacy engine (plan)

Status: **in progress — v2-only beta `1.6.3-beta.12`.** Make Attribution v2 the *only*
attribution engine and remove the content-hash guesser.

Beta progress (branch `attribution-v2`):
- **B3 done** — dual-run divergence machinery + `attribution-status` removed.
- **B1 done** — `Enabled()` is unconditional; the `BLAMELY_ATTRIBUTION_V2` opt-out and
  the v1-engine tests are gone. The guesser is now UNREACHABLE.
- **B2 pending** — physically delete the now-dead guesser body in `buildNote` + its
  helpers (a `buildNote`→`buildNoteV2` rewrite; the dead code has zero runtime effect,
  so this is cleanup, sequenced behind the v2 e2e tests).
- SQLite `content_sha` writes are intentionally KEPT for this beta (cheap, unused for
  attribution). Token/prompt/session metadata stays — reporting needs it.

Original plan below (steps 1–2 landed earlier; step 3 = B1).

## Why it can't be a blind delete
- `gitnotes.buildNote` (~930 lines) builds the note (files, ranges, totals) using the
  SQLite-recorded `content_sha` + budget/drift matcher. **v2 (`flip*`) rewrites that
  note's per-line authorship** — so deleting `buildNote` leaves v2 nothing to flip.
- `DiffCommit` is shared (v2 needs its ranges) — it stays.
- Un-captured edits (no working log) currently fall back to the v1 result. Removing it
  means they must default to **Human (I5)**.

## Target end-state
```
DiffCommit (ranges)  →  buildNoteV2 (skeleton: ranges + totals, every line Human)
                     →  flipNoteToWorkingLog / flipDeletions (v2 sets AI from the working log)
```
No `content_sha`, no budget/drift, no per-tool narrowing. Unobserved → Human.

## Ordered steps (each its own commit, suite green between)
1. **Gate the guesser.** In `buildNote`, skip the `content_sha`/drift/per-tool
   matching when `authorship.Enabled()` (default): added lines default Human, deletes
   default Human; the v2 flips then attribute. Keep the guesser only behind the
   opt-out. → default path is already v2 + honest fallback, no v1 guessing.
2. **Update v1-engine tests.** The ~8 suites that assert hash attribution set
   `BLAMELY_ATTRIBUTION_V2=0` (they now test the *fallback* explicitly).
3. **Make v2 mandatory.** `Enabled()` returns true unconditionally; remove the
   opt-out env. The guarded guesser from step 1 becomes dead code.
4. **Delete the dead guesser.** Remove the `content_sha`/budget/drift/per-tool code
   from `buildNote` (collapse it to `buildNoteV2`), plus the named retirees
   (`narrowToChangedLines`, `ResolveWholeFileWrite`, `netUnchangedEditLines`,
   `pickDriftAiEdit`, `pickEditForRemovedLine`, per-tool branches) and their tests.
5. **Prune the store/daemon.** With notes no longer reading `content_sha`, drop the
   SQLite edit/edit_lines write path (or keep only what watchers/reporting need),
   and simplify the daemon recording accordingly.
6. **Docs.** Fold this into the main design doc; mark v2 as the sole engine.

## Gate (do NOT start step 1 until this holds)
`blamely attribution-status` over the soak window shows agreement trending to 100%
with every divergence explained as **v2 correcting v1** — and the IDE + Windows
checks pass. That is the evidence that removing the fallback is safe.

## Reversibility
All on the `attribution-v2` branch; `main` is untouched. Each step is a separate
commit, so any step can be reverted independently.
