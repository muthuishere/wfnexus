# ADR 0013 — One API, two front ends

- **Status:** accepted
- **Date:** 2026-09-22

## Context

The platform needs a web UI (authoring, watching a run, approving) and a CLI
(running a workflow from a terminal, CI, another agent). The easy mistake is to
give the CLI its own path into the engine — it is right there, in the same
binary — and then discover the two front ends disagree about what a run is.

## Decision

**Both front ends are clients of the same REST API.** `bfp` speaks HTTP to the
server exactly as the browser does. There is no in-process shortcut.

The CLI shares the server's *types*, though: it decodes a workflow file into the
real `workflow.Definition` rather than a generic map.

## Why that last point is not a detail

`bfp validate` was written with `map[string]any`, and it rejected a workflow the
server loads happily:

```
$ bfp validate workflows/test-backfill.yaml
error: test-backfill: step "find-gaps" needs output_schema
```

YAML keys are snake_case (`output_schema`) and the API speaks the JSON names
(`outputSchema`). A generic map carries neither mapping, so **every multi-word
field was silently dropped** on the way to the server — `output_schema`,
`requires_approval`, `max_turns`, `ask_human`. The failure was loud here only by
luck: `output_schema` is required, so it complained. `requires_approval` would
have vanished in silence, and a workflow applied from the CLI would have run its
publish step with **no approval gate**.

Decoding through the shared struct makes the two spellings the same field by
construction.

## Consequences

- Anything the UI can do, the CLI can do, and neither can drift.
- The CLI imports `internal/workflow`, so it must live inside the same module.
- A generic map is never the right carrier for a typed document that crosses a
  format boundary. Where a struct exists, use it.
