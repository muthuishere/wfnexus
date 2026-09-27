# Recipe: dead-code-remover — a cleanup PR a reviewer can check line by line

Weekly: a baseline build and test count; an agent inventories unused code with
tool output as evidence; a second agent tries to PROVE each candidate is used; a
classifier decides per candidate (yes → remove, uncertain → a person, no → keep);
removal is one commit per candidate with build + tests after each; a PR writer's
claims are checked against the evidence; a person approves publishing.

## Use when

- "Nobody dares delete anything", "the codebase is full of dead code", "shrink the bundle".

## Not when

- No test suite at all — the proof is the suite; recommend `test-author` first.
- A public library whose exports are used by strangers — "unused here" is not unused.

## Evidence

Catalogue `development` §1 `code-remover`: dotnet/runtime "Removal/Cleanup 77
PRs 84.7%… Our highest success rate by category"; revert rate 0.6%. Build-these-
first #1. All six skills already exist.

## Questions — one at a time, offer the default

1. **Which repository, and which part of it?** → project + `{{SCOPE}}` *(default:
   the whole repo; a path scope like `src/` for a first run is smaller and safer)*
2. **Build and test commands?** Look them up from the repo first; confirm.
   → `{{BUILD_CMD}}`, `{{TEST_CMD}}` *(default: what the repo's Taskfile/Makefile/package.json says)*
   Toy check: "skip the tests" — without a suite there is no proof; refuse.
3. **What must never be touched?** → `{{NEVER_TOUCH}}` *(default: public API
   packages, generated code, migrations, anything referenced from config/templates)*
4. **How often?** → `{{CRON}}` *(default: weekly Monday 06:00 UTC; skipped if a
   `[dead-code]` PR is already open)*
5. **Max removals per PR?** → `{{MAX_REMOVALS}}` *(default: 10 — a reviewable PR)*
6. **Success number?** *(default: PR merge rate ≥ 80% and revert rate < 2%;
   catalogue baseline 84.7% / 0.6%)*

## Skeleton — `<name>/workflow.yaml`

Sidecars: `sidecars/route.py`, `questions.yaml`.

```yaml
name: {{NAME}}
description: >-
  Weekly evidence-based dead-code removal in {{SCOPE}}: inventory with tool
  evidence, an adversarial usage proof, a calibrated verdict per candidate, one
  commit per removal with build and tests after each, and a PR a person approves.
  Success: {{SUCCESS_NUMBER}}.

on:
  workflow_dispatch:
  schedule:
    - cron: "{{CRON}}"

input_schema:
  type: object
  properties:
    scope: { type: string, title: "Path scope", default: "{{SCOPE}}" }

steps:
  - id: baseline
    name: Baseline build and tests
    run: |
      if gh pr list --search "[dead-code] in:title" --state open --json number -q 'length' | grep -qv '^0$'; then
        echo "a [dead-code] PR is already open"; exit 4; fi
      ({{BUILD_CMD}}) && ({{TEST_CMD}}) 2>&1 | tail -40
    timeout_sec: 1800
    output_schema:
      type: object
      properties:
        ok:       { type: boolean }
        exitCode: { type: integer }
        stdout:   { type: string }
        stderr:   { type: string }
    gates:
      - field: exitCode
        equals: 4
        action: fail
        message: "A [dead-code] PR is already open — review it first."
      - field: ok
        equals: false
        action: fail
        message: "The baseline build or tests are red — nothing can be proven unused against a red suite."

  - id: find
    name: Inventory unused code
    needs: [baseline]
    skills: [dead-code-finder]
    tools: [read, grep, glob, bash]
    max_turns: 40
    prompt: |
      Inventory unused code in {{ .Input.scope }}, never in: {{NEVER_TOUCH}}.
      Evidence for every candidate is tool output. At most {{MAX_REMOVALS}} candidates.
    output_schema:
      type: object
      required: [candidates]
      properties:
        candidates:
          type: array
          items:
            type: object
            required: [id, symbol, file, evidence]
            properties:
              id:       { type: string }
              symbol:   { type: string }
              file:     { type: string }
              evidence: { type: string }

  - id: prove
    name: Try to prove each one IS used
    needs: [find]
    skills: [usage-prover]
    tools: [read, grep, glob, bash]
    max_turns: 40
    prompt: |
      Candidates: {{ json .Steps.find.candidates }}
      Search code, docs, templates, config, reflection and build tags for any use.
      Write candidates.jsonl — {"id", "state": "<symbol, file, finder evidence, every use you searched for and what you found>"}.
    output_schema:
      type: object
      required: [checked]
      properties:
        checked: { type: integer }

  - id: verdict
    name: Provably unused? (JEV)
    needs: [prove]
    run: |
      wfx judge -q ./questions.yaml --items candidates.jsonl > verdicts.jsonl || echo "judge failed"
      python3 route.py candidates.jsonl verdicts.jsonl unused
    timeout_sec: 180
    output_schema:
      type: object
      properties:
        ok:       { type: boolean }
        exitCode: { type: integer }
        stdout:   { type: string }
        stderr:   { type: string }

  - id: ask
    name: A person decides the uncertain ones
    needs: [verdict]
    when:
      - path: verdict.exitCode
        equals: 3
    tools: [read, bash, ask_human]
    max_turns: 10
    prompt: |
      ./uncertain.jsonl lists candidates the classifier could not settle. Ask the person,
      with ask_human, which to remove; append those lines to yes.jsonl.
    output_schema:
      type: object
      required: [approved]
      properties:
        approved: { type: array, items: { type: string } }

  - id: remove
    name: Remove, one commit each, green after each
    needs: [ask]
    skills: [safe-remover]
    tools: [read, write, edit, grep, glob, bash]
    max_turns: 60
    prompt: |
      Remove ONLY the candidates in ./yes.jsonl, one commit per candidate on a new branch,
      with its orphaned tests. After each commit run: {{BUILD_CMD}} && {{TEST_CMD}}.
      A red suite reverts that commit and records why. Never push.
    output_schema:
      type: object
      required: [branch, removed, reverted, suite_green, anything_removed]
      properties:
        anything_removed: { type: boolean }
        branch:      { type: string }
        removed:     { type: array, items: { type: string } }
        reverted:    { type: array, items: { type: string } }
        suite_green: { type: boolean }
    gates:
      - field: suite_green
        equals: false
        action: fail
        message: "The suite is not green after removal — nothing is published."

  - id: write_pr
    name: Write the PR, every claim checked
    needs: [remove]
    when:
      - path: remove.anything_removed
        equals: true
    skills: [cleanup-pr-writer]
    tools: [read, grep, glob, bash]
    max_turns: 20
    prompt: Write the PR description for branch {{ .Steps.remove.branch }} to pr.md, title starting "[dead-code]".
    output_schema:
      type: object
      required: [title, claims_verified]
      properties:
        title:           { type: string }
        claims_verified: { type: boolean }
    gates:
      - field: claims_verified
        equals: false
        action: needs_input
        message: "Some PR claims are not backed by the evidence — review pr.md."

  - id: publish
    name: Open the PR — a person approves
    needs: [write_pr]
    when:
      - path: remove.anything_removed
        equals: true
    requires_approval: true
    skills: [pr-publisher]
    tools: [read, bash]
    max_turns: 10
    prompt: Push {{ .Steps.remove.branch }} and open the PR titled "{{ .Steps.write_pr.title }}" with body pr.md.
    output_schema:
      type: object
      required: [pr_url]
      properties:
        pr_url: { type: string }

  - id: summary
    name: What happened this week
    needs: [publish]
    run: 'echo "removed: {{ json .Steps.remove.removed }} reverted: {{ json .Steps.remove.reverted }} pr: {{ .Steps.publish.pr_url }}"'
    output_schema:
      type: object
      properties:
        ok:       { type: boolean }
        exitCode: { type: integer }
        stdout:   { type: string }
        stderr:   { type: string }
```

## `questions.yaml`

```yaml
unused:
  type: noul
  instructions: Given the finder's evidence and the usage search, is this code provably unused at runtime?
  "true": no caller anywhere — code, tests that exercise real behaviour, templates, config, reflection, build tags
  "false": something uses it, even indirectly, or it is public API someone outside may call
```

## Gates

Open `[dead-code]` PR → stop. Red suite → fail, nothing published. Unverified PR
claims → needs_input. Publishing → `requires_approval`. Uncertain candidates → a person.

## Success number

PR merge rate (≥ 80%, baseline 84.7%) and revert rate (< 2%, baseline 0.6%).

## Bar check

trigger: weekly · record: the repo · output: PR with evidence table · verify:
usage proof + JEV + suite after each commit · gate: publish approval · no silent
failure: uncertain → person, red → fail · number: merge/revert rate.
