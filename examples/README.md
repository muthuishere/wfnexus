# Example workflows

These live in `.wfnexus/workflows/` — a workflow belongs in the repository it
acts on, the same arrangement as `.github/workflows/`, and for the same reasons:
it is reviewed in the pull request that changes it, it travels with a clone, and
a fork gets it for free.

Point the platform at any repository and its workflows become runnable:

```
wfx import https://github.com/you/your-repo --as yours
wfx import ./some/local/checkout --as dev      # used in place, so you can edit and re-run
wfx sources                                    # where workflows come from
wfx sources forget yours                       # the clone stays on disk
```

A workflow keeps its short name unless another source already took it; both are
always reachable fully qualified (`yours/checks`).

## What each one shows

| file | trigger | what it demonstrates |
|---|---|---|
| `checks.yaml` | `repository_dispatch`, `workflow_dispatch` | three parallel jobs of pure `run:` steps. **No model is called**, so it costs nothing — the right thing to run first. Also `runs-on:` and a per-step `shell:`. |
| `nightly-audit.yaml` | `schedule`, `workflow_dispatch` | the clock starts it, so the schedule carries the input nobody is there to type. One read-only agent, guardrailed against writing. |
| `on-issue.yaml` | `repository_dispatch` with `types:` | another system starts it. Two jobs, `needs:` between them, and the second reads the first's typed output. |
| `reusable-repro.yaml` | `workflow_call` only | a workflow other workflows invoke. Because `workflow_dispatch` is not declared, a person is told so rather than getting a confusing failure. |

## Triggers

`on:` is GitHub Actions' own block, with its own names and shapes — a bare name,
a list of names, or a mapping:

```yaml
on:
  workflow_dispatch:              # a person, the UI, wfx, the API
  schedule:
    - cron: "0 9 * * 1"           # five fields, UTC
  repository_dispatch:
    types: [bug_reported]         # another system, over the API
  workflow_call:                  # another workflow
```

A workflow with no `on:` is dispatch-only, so nothing starts firing on a timer
because a file gained a field.

**The trigger is enforced, not documented.** A workflow that lists only
`schedule:` cannot be started by a person, and one that lists only
`workflow_dispatch` cannot be started by an inbound POST.

Firing one from a forge's own Actions workflow is three lines:

```yaml
- run: |
    curl -X POST $WFX_URL/api/workflows/yours%2Fon-issue/dispatches \
      -H 'Content-Type: application/json' \
      -d '{"event_type":"issue_labelled","client_payload":{"issue_title":"...","issue_body":"..."}}'
```

`push` and `pull_request` are deliberately absent: they are one forge's event
vocabulary, and a `repository_dispatch` carries whatever the sender sends.
