# Which example to open

Every file in `examples/` is a real workflow that loads on this platform — a
test validates all of them through the same loader the server uses, so none of
them can rot into something that would not run.

Open the one whose SITUATION matches, not the one whose feature matches.

| the situation in front of you | open |
|---|---|
| "just run our checks / our CI" | `01-checks.yaml` |
| "tell me what kind of failure this was" | `02-judge-routes.yaml` |
| "don't waste an agent on rubbish input" | `03-decide-before-agent.yaml` |
| "do the whole job end to end, and don't let it publish anything I haven't seen" | `04-pipeline.yaml` |
| "it has to read half the repo to answer" | `05-team.yaml` |
| "the route depends on what it finds" | `06-goal-planned.yaml` |
| "run it nightly / when our other system fires / it needs a token" | `07-triggers-and-env.yaml` |
| "it has to build on the Windows box" | `08-runs-on-worker.yaml` |
| "it should take a different path / ask me when it's stuck" | `09-branching-and-asking.yaml` |

## What each one is really demonstrating

**01 — nothing costs anything.** Three jobs of pure `run:` steps, running at
once. No model is called. Start here when a command can answer the question,
and notice that it is a complete, useful workflow with no agent in it at all.

**02 — a verdict is cheap.** A command, then a `judge:` step: typed questions
on a small model, a fraction of a cent. Also shows `when:` skipping a step
entirely when the facts do not call for it, so a green suite costs nothing.

**03 — the cheap gate in front of the expensive one.** `decide:` runs BEFORE
the agent on the same step, so a fraction of a cent decides whether turns get
spent at all. The agent reads the verdict as `{{ .Decide.actionable }}`.

**04 — the typed hand-off.** The reason the platform exists. Five phases, four
contracts, and no phase can give work to the next until its output validates —
"I reproduced it" is a schema, not a claim. One human gate, at the only step
that reaches the outside world, with guardrails on everything before it.

**05 — a sub-agent keeps research out of the parent's context.** The explorer
reads and reports with file:line; the parent decides. Reach for this when one
step would otherwise fill its whole context with code it read once.

**06 — the order is derived.** `goal:` plus `consumes`/`produces`: the planner
works out the order and re-derives it after every step. Do not also write
`needs:` — that is a second, conflicting order.

**07 — started by something other than a person.** Three triggers that compose,
and a credential that is NAMED rather than written. `${GITHUB_PAT}` means
"whatever that variable holds where the step runs", which is what makes the
file safe to commit and safe to send to a worker.

**08 — work that must happen on a particular machine.** `runs-on:` is a label,
never a machine. The step's skills and tools travel to the worker; the CLI does
not — `provider: claude-cli` is that machine's PATH and that machine's
credential.

**09 — three different ways of not knowing**, which are not interchangeable:
`skip_to` when the workflow knows enough to take another route, `needs_input`
when it cannot proceed without a human, `ask_human` when the agent needs one
thing mid-step. `needs_input` parks the run durably — the answer can arrive
hours later.

## Using one

Copy the shape, not the words. A soul lifted from an example describes someone
else's engineer; the tools and the budget are for someone else's repository.
What transfers is the structure: where the gate is, what the schema promises,
which steps are commands.
