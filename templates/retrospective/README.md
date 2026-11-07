# retrospective

Looks back at the engine's runs and memory, says what broke, why, and
proposes typed changes — each one waits for a person.
Runs daily at 06:30, by hand, and when frontdesk is asked "why did X fail".

| step | what |
|---|---|
| `collect` | GETs on `$WFX_API` → `facts.json`: runs, failed steps, retries, approval waits, tokens/cost, every workflow's definition, **memory at every scope** (step, workflow, project, global) and state; the previous findings marked `recurred` / `no new failure`; `suppressed` proposal fingerprints |
| `analyze` | one agent turn (read only) → `{findings, proposals, summary}` |
| `check` | drops findings and proposals whose runs, steps or memory keys are not in the evidence, whose target does not fit, or whose fingerprint was rejected / is pending |
| `propose` | starts one **apply-mode** run of this workflow per proposal; each stops at its approval gate |
| `show` → `apply` | (apply mode) approve → `apply.py` makes the change through the API; reject → the run is cancelled |

**Proposal kinds:** `add-step`, `edit-step`, `new-workflow` (change =
the whole new definition, saved via `PUT /workflows/<name>`: a new version, or
a git proposal on a git-backed project), `memory-fix` (`{op: add|edit|delete,
id, body}`), `retire-workflow` (`DELETE /workflows/<name>`).

**Reject = never again.** The next review sees the cancelled apply run and
writes `rejected-proposal <fingerprint>` into this workflow's memory; the
fingerprint is kind + target, so a reworded proposal is still suppressed.

```sh
wfx runs --workflow retrospective   # awaiting_approval rows: one per proposal
wfx show <run-id>                   # the `show` step prints the proposal
wfx approve <run-id>    |    wfx reject <run-id> -m "why not"
```

**Input (all optional):** `workflow`, `runId`, `question`, `since_hours`
(review); `mode: apply`, `proposal`, `fingerprint` are set by `propose`.

**Fill:** `SINCE_HOURS`; `WFX_TOKEN` when the API needs a token (it must
be allowed to save workflows and write memory for approved proposals to apply).
