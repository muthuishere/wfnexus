# Pending: toolnexus bump

`apps/api` is pinned to published **v0.18.1** and stays there until the platform
is complete. `spikes/` keeps a `replace` onto the local toolnexus working tree,
because that module exists to verify unreleased behaviour.

## When a release carries issues #87–#93

1. Bump `apps/api/go.mod`, drop the `replace` in `spikes/go.mod`.
2. Re-run `spikes/07-verify` (13 assertions) and the platform's 55 tests.
3. **Add the two assertions that did not exist at verification time.** Both
   changed after our run, and both are the opposite of what a reader would
   assume, so they are worth pinning:

   - **#90 — the `limit` vocabulary is closed and canonical.** Assert the exact
     spelling: `maxTurns`, `maxTokens`, `maxToolCalls`, `maxWallMs`,
     `maxChildren`, `maxConcurrent`, `maxDepth`, `completion`, `timeout`. Four
     ports were emitting four different strings in the very field #90 asked for
     so hosts could branch on it.
   - **#88 — `Turns` is the handle's OWN cumulative round trips, NOT a subtree
     total.** This is deliberately unlike `TotalTokens` sitting next to it,
     which *is* the subtree. Assert the difference explicitly.

4. Re-check the skill registry: the duplicate-name winner now sorts by **depth
   ascending, then code point** (A15). Our registry's shadowing order depends on
   it, and a `docx`-only fixture is green under both the old and new rules — so
   any test we write must use a name that sorts *after* the nested directory's
   first segment (`synced`), e.g. `xlsx`.

## Behaviour that changes when we bump (reported by upstream, 2026-09-22)

Four of these reach a host that reads tool output. Assert them when we bump.

- **`grep` now emits a RELATIVE, slash-separated path** (`rel:line:text`), not a
  machine-absolute one. Five of five ports had been sorting on the relative path
  and printing the absolute one.
- **`glob`, `grep` and the `<skill_files>` sample SORT BEFORE CAPPING.**
  Previously the walk broke at the limit, so the *filesystem* chose which
  results the model saw — a different set on a different machine, and on some
  filesystems between two runs of the same process.

  The precise consequence, which is sharper than "the order changed": **the cap
  now selects from a sorted set instead of from whatever the walk reached
  first.** For any skill with more resources than the cap, a step may now see
  files it has never seen and **lose files it relied on**. Check `bug-fix` and
  `test-backfill` against this when we bump — both lean on skills with sibling
  files.
- **Provider errors are typed** (`ProviderError` with status / body / retryAfter,
  account identifiers redacted). Our issue #92 is why. Our `scrub()` stays as
  defence in depth, but error matching moves.
- **The duplicate-skill winner flipped**: a top-level skill now always beats a
  nested copy of the same name — so a name may resolve to a different **body**.

## Open upstream, worth watching

`read`'s offset/limit and the `<skill_files>` sample were explicitly ruled out
of upstream's ordering work, so their behaviour under a future execution seam is
undecided. If we ever depend on a skill's sibling files being a *stable* set,
that is the thing to pin first.

## Also parked

`apps/api/internal/devinadapter` is another session's work, behind the
`toolnexus_inprocess` build tag: it needs `InProcessTransport` (toolnexus issue
#95), which is not in v0.18.1.

```sh
go test -tags toolnexus_inprocess ./internal/devinadapter/
```

Drop the tag once a release carries the export.
