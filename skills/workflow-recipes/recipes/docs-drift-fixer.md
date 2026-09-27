# Recipe: docs-drift-fixer — docs that stay true to the code

Daily: a command lists code changed since the last run and the docs that
mention it; one agent drafts doc edits citing the changed code line; the docs
build and link check run; a classifier checks every edit against the code; a
person approves the docs PR.

## Use when

- "Our README/API docs lie", "people follow the docs and it breaks".

## Not when

- Docs are generated from code already (OpenAPI → site) — fix the generator instead.

## Evidence

Catalogue `development` §2 `docs-drift-fixer`: GitHub's factory "Daily
Documentation Updater… 57 merged PRs out of 59 proposed (96% merge rate)";
dotnet Documentation 68.1%. Build-these-first #2.

## Questions — one at a time, offer the default

1. **Which docs?** → `{{DOCS_GLOB}}` *(default: `README.md docs/**/*.md`)*
2. **Which code do they describe?** → `{{CODE_GLOB}}` *(default: `src/ api/ cmd/`)*
3. **How do we build/check the docs?** → `{{DOCS_CHECK_CMD}}` *(default: a link
   check, e.g. `npx -y markdown-link-check -q README.md`; the docs site build if there is one)*
   Toy check: "no check" — then nothing proves an edit did not break the docs; ask for at least a link check.
4. **How often?** → `{{CRON}}` *(default: daily 05:00 UTC, looking at commits since the last run)*
5. **Success number?** *(default: docs-PR merge rate ≥ 80%; baseline 96% in GitHub's factory)*

## Skeleton — `<name>/workflow.yaml`

Sidecars: `sidecars/route.py`, `questions.yaml`.

```yaml
name: {{NAME}}
description: >-
  Daily: finds docs in {{DOCS_GLOB}} that no longer match code changed in
  {{CODE_GLOB}}, drafts cited edits, checks each against the code and opens a
  docs PR a person approves. Success: {{SUCCESS_NUMBER}}.

on:
  workflow_dispatch:
  schedule:
    - cron: "{{CRON}}"

input_schema:
  type: object
  properties:
    since: { type: string, title: "Since (git ref; empty = the commit the last run saw)" }

steps:
  - id: changed
    name: Code changed since last time, and the docs that mention it
    run: |
      # stdout is ONLY the commit this run saw — it becomes the watermark below.
      SINCE="{{ .Input.since }}"; [ -n "$SINCE" ] || SINCE="{{ default "HEAD~20" .Workflow.last_sha }}"
      git diff --name-only "$SINCE"..HEAD -- {{CODE_GLOB}} | sort -u | grep . > changed.txt || true
      printf %s "$(git rev-parse HEAD)"
      test -s changed.txt || { echo "no code changes" >&2; exit 0; }
      for f in $(cat changed.txt); do grep -l "$(basename "${f%.*}")" {{DOCS_GLOB}} 2>/dev/null; done | sort -u > docs.txt || true
      test -s docs.txt && exit 3 || exit 0
    timeout_sec: 120
    state:
      workflow:
        last_sha: "{{ .Output.stdout }}"
    output_schema:
      type: object
      properties:
        ok:       { type: boolean }
        exitCode: { type: integer }
        stdout:   { type: string }
        stderr:   { type: string }

  - id: draft
    name: Draft cited doc edits
    needs: [changed]
    when:
      - path: changed.exitCode
        equals: 3
    skills: [repo-navigator]
    tools: [read, write, edit, grep, glob, bash]
    max_turns: 40
    prompt: |
      ./changed.txt lists changed code; ./docs.txt the docs that mention it. On a new
      branch, fix only statements the code now contradicts. Every edit cites the code
      file:line it follows. Write edits.jsonl — {"id": "<doc>#<n>", "state": "<old text,
      new text, cited code line and what it says>"}. Never push.
    output_schema:
      type: object
      required: [branch, edits]
      properties:
        branch: { type: string }
        edits:  { type: integer }

  - id: check
    name: Docs build and link check
    needs: [draft]
    when:
      - path: changed.exitCode
        equals: 3
    run: "{{DOCS_CHECK_CMD}}"
    timeout_sec: 300
    output_schema:
      type: object
      properties:
        ok:       { type: boolean }
        exitCode: { type: integer }
        stdout:   { type: string }
        stderr:   { type: string }
    gates:
      - field: ok
        equals: false
        action: fail
        message: "The docs check failed on the drafted edits."

  - id: verdict
    name: Does each edit match the code? (JEV)
    needs: [check]
    when:
      - path: changed.exitCode
        equals: 3
    run: |
      wfx judge -q ./questions.yaml --items edits.jsonl > verdicts.jsonl || echo "judge failed"
      python3 route.py edits.jsonl verdicts.jsonl matches_code
    timeout_sec: 180
    output_schema:
      type: object
      properties:
        ok:       { type: boolean }
        exitCode: { type: integer }
        stdout:   { type: string }
        stderr:   { type: string }
    gates:
      - field: exitCode
        equals: 3
        action: needs_input
        message: "Some doc edits could not be confirmed against the code — see uncertain.jsonl and say which to keep."

  - id: publish
    name: Open the docs PR — a person approves
    needs: [verdict]
    when:
      - path: changed.exitCode
        equals: 3
    requires_approval: true
    skills: [pr-publisher]
    tools: [read, bash]
    max_turns: 10
    prompt: |
      Revert any edit listed in ./no.jsonl, then push {{ .Steps.draft.branch }} and open a
      docs PR listing each kept edit with its cited code line.
    output_schema:
      type: object
      required: [pr_url]
      properties:
        pr_url: { type: string }
```

## `questions.yaml`

```yaml
matches_code:
  type: noul
  instructions: Does the new doc text say what the cited code line actually does?
  "true": the cited code clearly implements exactly what the new text claims
  "false": the code says something else, the citation is missing, or the edit changes meaning beyond the drift
```

## Gates

Docs check red → fail. Any uncertain edit → needs_input. PR → approval; wrong
docs mislead users, so nothing is pushed to main.

## Success number

Docs-PR merge rate (≥ 80%; factory baseline 96%).

## Bar check

trigger: daily · record: repo · output: docs PR · verify: docs check + JEV per
edit · gate: PR approval · no silent failure: uncertain → needs_input · number: merge rate.
