# ADR 0008 — Fix upstream, and verify it from outside

- **Status:** accepted
- **Date:** 2026-09-22

## Context

Building on toolnexus surfaced seven defects, several of them silent: a
guardrail that did not run on one of two documented entry points, a token total
that omitted the whole sub-agent tree, a resume that discarded the human's
answer and reported `done`.

Two responses were possible: work around each locally, or report them.

## Decision

**Report and fix upstream, then verify from outside as a consumer.**

`spikes/07-verify` asserts each fix against the upstream working tree with a
scripted `http.RoundTripper` — offline, so a failure is behaviour and never a
flaky network. `spikes/08-registry` measures the real machine skill registry.

## What this produced, and what it cost

Seven issues filed, seven fixed across seven language ports. The outside-in
check found things a green CI could not: a fixture that was green under both the
old and the new sort rule, so the spec asserted an ordering its own comparison
could not deliver.

It also cost us being wrong in public **five times**:

- diagnosed a classifier failure as a bad default when it was our own
  base/model mismatch;
- asserted a timeout status from the wrong one of two vocabularies both spelled
  `status`;
- reported a symlink as the cause of a shadowing bug that was alphabetical
  ordering against a sibling directory;
- called a colleague's real symbol invented after grepping a single checkout;
- inferred issue numbers instead of querying the issue tracker.

## Consequences

- **Verify against the registry of record, not one local copy.** Four of the
  five errors above are the same error: reasoning confidently from a single
  source.
- A consumer test is worth writing even when upstream's CI is green, because it
  fails for different reasons.
- Report observations with high confidence and mechanisms with low confidence.
  Every observation above held; most proposed mechanisms did not.
