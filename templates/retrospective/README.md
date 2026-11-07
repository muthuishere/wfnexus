# retrospective

Looks back at the engine's runs and says what broke, why, and what to change.
Runs daily at 06:30, by hand, and when frontdesk is asked "why did X fail".

| step | what |
|---|---|
| `collect` | read-only GETs on `$WFX_API` → `facts.json`: runs per workflow, failed steps, retries, approval waits, tokens and cost, and the last retrospective's findings marked `recurred` or `no new failure` |
| `analyze` | one agent turn (read only) → `{findings:[{workflow, runId, stepId, what, why, fix}], summary}` |
| `check` | drops any finding whose run or step is not in the evidence; prints `{findings, summary, dropped, fixTracking}` |

**It proposes, never edits.** The agent has only `read`; each `fix` is a
concrete change for a person or a PR workflow to make.

**Did the fix work?** Each run reads the previous retrospective's findings
back and checks whether that workflow failed again since. The ones that did
are `recurred`, and the summary says the fix did not hold.

**Input (all optional):** `workflow`, `runId`, `question`, `since_hours`.
frontdesk passes these when it delegates with `start: retrospective` (`wait: true`).

**Fill:** `SINCE_HOURS`; `WFX_TOKEN` when the API needs a token.
