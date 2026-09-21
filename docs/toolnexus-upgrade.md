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

## Also parked

`apps/api/internal/devinadapter` is another session's work, behind the
`toolnexus_inprocess` build tag: it needs `InProcessTransport` (toolnexus issue
#95), which is not in v0.18.1.

```sh
go test -tags toolnexus_inprocess ./internal/devinadapter/
```

Drop the tag once a release carries the export.
