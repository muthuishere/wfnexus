# ADR 0016 — A real sandbox, one per run, and the retirement of ADR 0006

- **Status:** accepted
- **Date:** 2026-09-22
- **Supersedes:** ADR 0006 (which stays as the record of why a guardrail is not
  enough)

## Context

ADR 0006 is honest about its own limit: containment is implemented as a
guardrail, and a guardrail enumerates escapes. It grew a `cd` rule after an
agent walked into the platform's own repository, then grew an absolute-path rule
after five more escapes were found in public code. Each addition was correct and
each one proved the shape wrong. The lesson recorded there, from peer research,
is the whole argument for this ADR:

> every mechanism that fails enumerates escapes; every mechanism that works
> enumerates inclusions.

With ADR 0015's runner in place there is finally a process to put inside
something.

## Decision

**Stop exec'ing `bash` on the host. The step's tools run inside a sandbox whose
filesystem view does not contain anything we would have had to deny.** The
platform's own repository is not in the mount namespace, so the escape that
started all of this is not denied — it is absent.

Three measured facts shape the design. They were measured by a peer and are
taken as given rather than re-derived:

**One sandbox per RUN, with an exec per tool call — not a container per step.**
`docker run --rm` costs 307 ms against 50.8 ms for a warm `docker exec` and
5.3 ms for a plain fork+exec. A container per tool call is ~60× a fork, so a
step making 30 tool calls would spend 9–30 s on nothing but lifecycle. Every
fast number published in this field (E2B ~150 ms, Daytona ~90 ms) is a warm pool
or a snapshot resume, never a cold start. The run is the right lifetime because
it is not a new concept: **a run already owns a git worktree, so it may as well
own the sandbox that holds that worktree.**

**The hardening flags are nearly free.** Namespace setup is 7.94 ms and
`--network=none` is 0.04 ms. So `--read-only --cap-drop=ALL --network=none
--pid=private` is the default, not a tier. The expense is the container
lifecycle, never the isolation.

**macOS Seatbelt is a real boundary we can have today.** Unprivileged, ~12.4 ms,
and it enumerates inclusions — which is exactly why it closes the escapes ADR
0006 records, on the machines this is actually developed on, long before a
container path lands.

**Seatbelt fails closed.** It cannot nest inside an already-sandboxed host, so
if wfnexus itself is ever run inside a sandbox the profile silently cannot be
applied. In that case the step **refuses to run**. A containment mechanism that
degrades quietly to "no containment" is worse than having none, because the ADR
still says the step was protected and nothing in the run says otherwise.

## What replaces ADR 0006

The guardrail is not deleted. It stays as **defence in depth and as the
in-process mode's only protection** (ADR 0015 keeps an in-process mode for
tests and single-step CLI runs). But it stops being the security model, and its
documentation stops claiming to be one. ADR 0006's escape table becomes a
regression suite: every entry must now fail because the path does not exist,
which is a stronger assertion than "because a rule matched".

## Consequences

- **A run's sandbox has state that outlives a step.** Two steps in a run share a
  filesystem, which is what makes hand-off through the worktree work at all; it
  also means a step can be poisoned by an earlier step in the same run. That is
  the same trust boundary a developer's laptop has, and it is drawn at the run.
- Parallel steps within a run (ADR 0012) exec concurrently into one sandbox.
  They are not isolated from each other. If that is ever needed, it is a
  sandbox-per-step decision with the 307 ms price attached, taken deliberately.
- Tool-call latency rises from ~5 ms to ~51 ms on the container path. At tens of
  calls per step that is single-digit seconds — acceptable, and stated here so a
  later "why is it slower" has an answer.
- The three failure kinds must be explicit; see ADR 0016a. Without them, retry
  cannot tell "image missing" from "the model's command was wrong".
