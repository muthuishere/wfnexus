# ADR 0019 — Horizontal scale without a broker

- **Status:** accepted
- **Date:** 2026-09-22

## Context

One process runs everything: it serves the API, parses the workflows, and
executes every step as a goroutine, bounded by `MaxConcurrentRuns`. That cap is
the only thing between a burst of reports and a thrashed machine, and it is a
per-machine cap, so the answer to "more load" is currently "a bigger machine".

Runs are also lost on restart, because the work lives in the process that
accepted it. ADR 0015 fixes the second half of that by making a step a launched
unit rather than a goroutine; this ADR makes the *claim* on that unit survive
the process too.

The obvious move is a broker — Redis, NATS, SQS. We already require Postgres for
run state, and a second piece of infrastructure has to earn its place.

## Decision

**Postgres is the queue. N engine processes claim runs with
`SELECT … FOR UPDATE SKIP LOCKED`, hold a lease, and heartbeat it.**

- A worker claims the oldest `queued` run whose lease is free, in one statement,
  and `SKIP LOCKED` means concurrent claimers never block each other and never
  hand out the same run twice. This is Postgres doing what a queue does, on data
  that is already there and already transactional with the run's own state — a
  broker would put the claim in one system and the state in another, and the two
  would disagree under partition.
- The claim carries a **lease with an expiry**, refreshed by a heartbeat while
  the run executes. A worker that dies stops heartbeating; the lease expires and
  another worker claims the run. No coordinator decides this.
- **A lease expiry is not a failure.** The run returns to `queued` with its
  completed steps intact — the step is the durability boundary (ADR 0005), so
  recovery starts at the first step that did not finish, and ADR 0018's snapshot
  is what makes that cheap.
- **Still one binary.** `wfnexus serve` (API + worker), `wfnexus worker`
  (worker only). The same choice as ADR 0013 for the front ends and ADR 0015 for
  the runner: modes of one artefact, never a second artefact to keep in step.
- `MaxConcurrentRuns` stays and becomes what it always should have been: a
  **per-worker** machine-load bound, not a system capacity limit. System
  capacity is the number of workers.

## What this deliberately does not do

**No fair scheduling, no priorities, no per-tenant quotas.** Claiming is
oldest-first. Priorities are a real requirement the moment there is more than
one tenant (ADR 0020), and an `ORDER BY priority, created_at` is a one-line
change to make then — with a policy behind it — rather than a guess made now.

**No step-level distribution.** The unit of claim is the **run**, not the step,
even though ADR 0012 runs steps in parallel. A run owns a git worktree and, from
ADR 0016, a sandbox; splitting its steps across machines would mean shipping
that filesystem between them. The run is the unit because the filesystem is.

## Consequences

- Throughput scales with worker count until Postgres is the bottleneck, which
  for claim-rate arithmetic on this workload is far past where a single machine
  stops being enough.
- **A heartbeat interval and a lease TTL are now correctness parameters.** Too
  short and a busy worker loses runs it is still executing; too long and a dead
  worker's runs stall for the TTL. The lease must be comfortably longer than the
  longest gap between heartbeats, and the heartbeat must not be on the same
  goroutine as the work.
- A run reclaimed after a lease expiry can double-execute a step that was
  mid-flight on a worker that is slow rather than dead. ADR 0012's idempotency
  requirement is what makes that survivable, and ADR 0016a's `unavailable` kind
  is what keeps it from being counted as a step failure.
- Postgres becomes load-bearing for availability, not just for state. It already
  was, in practice; this makes it explicit.
