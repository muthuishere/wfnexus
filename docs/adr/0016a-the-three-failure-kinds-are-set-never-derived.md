# ADR 0016a — The three failure kinds are SET, never derived

- **Status:** accepted
- **Date:** 2026-09-22

## Context

ADR 0012 gives a step a retry policy. A retry policy is only as good as its
ability to distinguish a flaky worker from a bad command, and an exit code
cannot make that distinction:

- A container that runs and exits `125` is **byte-identical** to a container
  that never started because the image was missing — 125 is Docker's own "could
  not run" code and also a perfectly legal exit code for the program inside.
- A macOS Seatbelt denial surfaces as `EPERM` inside the process while the
  wrapper exits `0`. The step looks successful and the work did not happen.

Retrying the first is right and cheap. Retrying the second is right only after
a delay. Retrying the third burns budget on something that can never succeed.

## Decision

**The execution layer SETS one of three kinds. Nothing downstream infers it.**

| kind | meaning | retry |
|---|---|---|
| `unavailable` | could not run at all — image missing, sandbox refused, profile could not be applied, no worker | retry after `retry_after_ms`; does not count against the step's attempts |
| `exited` | ran to completion and the outcome was a failure | counts against `retry.max_attempts` |
| `invalid` | the model's command or arguments were wrong | never retried at this layer; it goes back to the model, which is the only thing that can fix it |

Following containerd's shape: **a create-failure is an error; an exit is an
event and is never an error.** The two travel on different channels, so they
cannot be conflated by a caller that forgot to check.

`unavailable` carries a `reason` and a `retry_after_ms`, which is close to
Cloudflare's `ContainerUnavailableError{reason, retryAfterMs}` and is precisely
what ADR 0012's backoff needs to stop guessing.

E2B is the counter-example to copy nothing from here: it raises on a non-zero
exit, which collapses `unavailable` and `exited` into one thing and leaves the
caller to re-derive the difference from a message string.

## Consequences

- A `unavailable` that repeats is an **infrastructure** alert, not a workflow
  failure, and the run stays queued rather than failing. This is the behaviour
  ADR 0019's multi-worker world requires: one sick worker must not fail work
  that another could do.
- Seatbelt's silent-EPERM case is why ADR 0016 fails closed: without a set kind
  there is no way to turn "the profile did not apply" into `unavailable`, and it
  would arrive as a successful step.
- Every executor we add — in-process, subprocess, container, remote worker —
  must set the kind explicitly. A default is deliberately not provided; a missing
  kind is a programming error and is rejected, because a default would be a
  guess wearing a type.
