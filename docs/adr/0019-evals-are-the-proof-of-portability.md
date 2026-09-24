# ADR 0019 — Evals are the proof of portability, not a quality feature

- **Status:** proposed
- **Date:** 2026-09-24

## Context

The product claim, restated in [the pivot](../research/the-pivot-2026-09-24.md) §2.2, is:
**build the workflow once, run it against Codex, opencode, Claude CLI, or your own
API.** That sentence is unverifiable today. Nothing in this repository can show
that a workflow which passed on sonnet still passes on a smaller local model.

ADR 0003 gets us halfway and no further, and the halves must not be confused:

- **The contract gate validates identically on every backend.** `submit_output`
  is a native tool whose input schema is the step's `output_schema`; the
  validator does not know or care which model produced the object. Portability
  of the *contract* is already true.
- **Capability does not.** A step that submits a valid object in 4 turns on
  sonnet may burn its entire `max_attempts` budget on a smaller model, or submit
  an object that is schema-valid and factually wrong — a `reproduce-bug` output
  with a `test_command` that does not reproduce anything.

**Schema validity is not correctness.** That distinction is the whole reason
this ADR exists. Without something that tests the second thing, "runs anywhere"
is a claim about our validator, not about the workflow.

Mastra ships `mastra scorers`. We have nothing. Until today that was recorded as
a gap we chose to skip; under the pivot it is load-bearing, because it is the
only mechanism that turns the differentiator into evidence.

## Decision

**An eval is a recorded set of inputs plus assertions over the validated step
output.** Nothing more. Specifically:

- assertions run against the typed object already persisted in
  `step_runs.output jsonb` (`migrations/000001_init.up.sql:22`) — **not** over
  prose, **not** over the transcript, **not** over the diff;
- an assertion is therefore a **predicate over JSON** (`has_tests == true`,
  `len(areas) >= 2`, `test_command` matches), evaluated with no model call;
- an eval names a workflow, a corpus of inputs, and the backend(s) to run it on.

This is where ADR 0003 pays a dividend nobody has claimed yet. Everyone else's
step emits prose, so their eval needs an LLM judging prose — which is slow,
dear, and itself nondeterministic across the very backends under test. Ours
already emits a machine-checkable object, so **the cheapest possible assertion
is also the strongest one available.** An eval over a typed output costs a
comparison; an eval over prose costs another inference.

### The headline artifact: the portability matrix

One workflow × N backends, one row each: did every step submit, in how many
turns against its budget, and at what cost.

| backend | steps submitted | turns / budget | $ |
|---|---|---|---|
| sonnet-4.5 (openrouter) | 2/2 | 31 / 50 | 4.37 |
| opencode (haiku-4.5) | ? | ? | ? |
| claude-cli | ? | ? | ? |

That table **is** the marketing claim and the regression test at the same time,
which is the reason to build this one thing rather than a scoring framework.
The cost column exists only because the pivot's §4 finding gets fixed first —
`MetricEvent` already carries the tokens and we discard them.

### Reuse before inventing

Most of the harness is already here, and this ADR invents no parallel mechanism:

- `registries.json` `classifiers` already carries a **`recorded`** entry,
  `backend: static` — *"Replays a recorded corpus. No network, no credential —
  what tests use."* It resolves to toolnexus `StyleStatic`
  (`classifier.go:80`), which keys a `RecordedDecision` corpus on the canonical
  request. Record/replay of a judge exists; we did not have to build it.
- The **`decide`** step kind (`internal/engine/decide.go`) already runs typed
  questions — `noul` / `choice` / `score` — and `decideGate` already thresholds
  the answers (`below` / `at_least` / `is`). That is a scorer with a pass mark,
  shipped, in production use as a pre-agent gate.
- `wfx dryrun` already resolves budgets and prints the per-step ceiling, so the
  matrix's budget column needs no new accounting.

**What is genuinely missing is small:** a corpus format, a runner that executes
the same workflow N times across named backends, the assertion predicate, and
the table. Notably, `step.Classifier` is parsed and validated but **never
selects a backend at run time** — `Engine.classifier()` always builds from
config, and `UseClassifier` is a test-only override. Honouring the named entry
is a prerequisite and is a few lines, not a subsystem.

### The three-scale test

- **One person.** An eval must run locally against a cheap or local model and
  must not require an account. The `recorded` classifier and the local CLI
  backends already make this true; nothing in the design may add a hosted call.
- **A small org.** Evaluate a workflow **before it is published** to the shared
  registry — the publish gate on `wfx publish` (pivot §3). A workflow that
  nobody could run is not shareable, and the registry is where that is caught.
- **An enterprise.** Prove a workflow still passes on **their** model —
  self-hosted, air-gapped, whatever they were told to use. This is the
  procurement conversation, and it is **the strongest commercial argument in
  this ADR**: the buyer does not want our benchmark, they want a passing matrix
  row for the model their security team already approved, produced on their own
  hardware. No hosted competitor can hand them that.

### Cost honesty

Evals cost tokens by definition. The split must be stated every time, because
the README currently borrows confidence from one and spends it on the other:

| | `wfx dryrun` | eval |
|---|---|---|
| what | static, structural | a real run, behavioural |
| cost | nothing | real money |
| proves | the workflow is *well-formed* | the workflow *works there* |

They are complementary, not alternatives. A dry run cannot tell you a model is
too small; an eval cannot be run on every commit for free. Selling either as
the other is the mistake to avoid.

## Consequences

- The portability claim becomes falsifiable — including against us. A matrix
  with an empty column is an honest answer, and it is also the pivot's §7 kill
  criterion arriving as data rather than as a hunch.
- Adopted vocabulary, per "adopt, never invent": **scorer** and **eval** from
  Mastra, **fixture / assertion / suite** from ordinary test tooling,
  **attempt = one span** from Trigger.dev/Inngest. No new nouns.

## Not decided here

- **Scoring rubrics.** Whether a matrix cell is pass/fail or a number, and who
  sets the pass mark. `decide`'s thresholds are a candidate, not a decision.
- **Flakiness and variance.** Agents are nondeterministic; one run is one
  sample. How many runs make a cell, and how a near-budget pass is reported, is
  open. This is the hardest unsolved part and it is not being hand-waved.
- **CI integration.** Whether evals run on a schedule, on publish, or on a tag,
  and against which budget. Distribution (pivot §5.1) has to exist first.
