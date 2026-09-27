# Recipe: dependency-upgrade-repair — bump, fix what breaks, prove it

Weekly: list outdated dependencies and baseline the suite; an agent bumps one
group, reads the changelog and repairs call sites; the full suite runs; a
classifier checks that tests were not weakened to get green; a person approves
the PR.

## Use when

- "Dependabot PRs pile up because each one breaks something."
- "We're three majors behind on X and nobody has the afternoon."

## Not when

- A security alert on one package that just needs a patch bump → Dependabot alone is enough.
- No test suite — the suite is the judge; say so.

## Evidence

Catalogue `development` §4 `dep-upgrade-repair`: dotnet "Update/Upgrade 44 PRs
67.4%" merged; "Review dependency update PRs for breaking changes" (agentics);
GitHub 2026-04: Dependabot alerts assignable to agents. Build-these-first #4.

## Questions — one at a time, offer the default

1. **Which repository and ecosystem?** Detect from lockfiles; confirm. → `{{ECOSYSTEM}}`
2. **What to upgrade?** → `{{UPGRADE_SCOPE}}` *(default: every minor/patch in one
   group; majors one per run, named explicitly)*
3. **Outdated-list command?** → `{{OUTDATED_CMD}}` *(default by ecosystem:
   `npm outdated --json`, `go list -u -m -json all`, `pip list --outdated --format json`)*
4. **Build and test commands?** → `{{BUILD_CMD}}`, `{{TEST_CMD}}` (from the repo).
   Toy check: "just bump the versions" without a suite run — that is Dependabot; refuse.
5. **Schedule?** → `{{CRON}}` *(default: weekly Tuesday 06:00 UTC)*
6. **Success number?** *(default: upgrade-PR merge rate ≥ 65% — baseline 67.4%; weeks behind latest trending down)*

## Skeleton — `<name>/workflow.yaml`

Sidecars: `sidecars/route.py`, `questions.yaml`.

```yaml
name: {{NAME}}
description: >-
  Weekly {{ECOSYSTEM}} upgrade: bumps {{UPGRADE_SCOPE}}, repairs what breaks,
  proves it with the full suite, checks the tests were not weakened, and opens
  one PR a person approves. Success: {{SUCCESS_NUMBER}}.

on:
  workflow_dispatch:
  schedule:
    - cron: "{{CRON}}"

input_schema:
  type: object
  properties:
    only: { type: string, title: "Only these packages (empty = the scope above)" }

steps:
  - id: baseline
    name: Outdated list and a green baseline
    run: |
      {{OUTDATED_CMD}} > outdated.json || true
      ({{BUILD_CMD}}) && ({{TEST_CMD}}) 2>&1 | tail -30
    timeout_sec: 1800
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
        message: "The suite is red before any upgrade — fix main first."

  - id: upgrade
    name: Bump and repair
    needs: [baseline]
    skills: [dep-upgrader]
    tools: [read, write, edit, grep, glob, bash]
    max_turns: 60
    prompt: |
      ./outdated.json lists outdated packages. Scope: {{UPGRADE_SCOPE}} {{ .Input.only }}.
      On a new branch, bump them, read each changelog, and repair call sites until
      `{{BUILD_CMD}} && {{TEST_CMD}}` is green. A bump you cannot make green is dropped
      and recorded. Never edit a test's assertion to make it pass. Never push.
      Write change.jsonl — ONE line {"id": "upgrade", "state": "<git diff --stat, every test
      file changed and how, dropped bumps>"}.
    output_schema:
      type: object
      required: [branch, bumped, dropped, suite_green]
      properties:
        branch:      { type: string }
        bumped:      { type: array, items: { type: string } }
        dropped:     { type: array, items: { type: string } }
        suite_green: { type: boolean }
    gates:
      - field: suite_green
        equals: false
        action: fail
        message: "Could not get the suite green — nothing is published."

  - id: suite
    name: Prove it — full suite on the branch
    needs: [upgrade]
    run: ({{BUILD_CMD}}) && ({{TEST_CMD}}) 2>&1 | tail -30
    timeout_sec: 1800
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
        message: "The agent said green; the suite says red."

  - id: verdict
    name: Tests weakened? (JEV)
    needs: [suite]
    run: |
      wfx judge -q ./questions.yaml --items change.jsonl > verdicts.jsonl || echo "judge failed"
      python3 route.py change.jsonl verdicts.jsonl clean_upgrade
    timeout_sec: 120
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
        message: "Unsure whether the upgrade changed behaviour or loosened tests — review the diff."
      - field: exitCode
        equals: 4
        action: fail
        message: "The classifier says tests were weakened or behaviour changed — not publishing."

  - id: publish
    name: Open the PR — a person approves
    needs: [verdict]
    requires_approval: true
    skills: [pr-publisher]
    tools: [read, bash]
    max_turns: 10
    prompt: |
      Push {{ .Steps.upgrade.branch }} and open one PR: bumped {{ json .Steps.upgrade.bumped }},
      dropped {{ json .Steps.upgrade.dropped }}; close superseded bot PRs in the body.
    output_schema:
      type: object
      required: [pr_url]
      properties:
        pr_url: { type: string }
```

## `questions.yaml`

```yaml
clean_upgrade:
  type: noul
  instructions: Is this purely a dependency upgrade with call-site repairs, with no test weakened and no behaviour change beyond the upgrade?
  "true": tests unchanged or only adapted to a renamed API; no assertion loosened, skipped or deleted
  "false": assertions loosened, tests skipped or deleted, or behaviour changed beyond what the upgrade requires
```

## Gates

Red baseline → fail. Red after repair → fail. Weakened tests → fail; uncertain →
needs_input. PR → approval. Every gate before `publish` ends the run, so
`publish` needs no `when:`.

## Success number

Upgrade-PR merge rate (≥ 65%, baseline 67.4%); weeks behind latest.

## Bar check

trigger: weekly · record: repo + lockfiles · output: one PR · verify: suite
before/after + JEV "not weakened" · gate: PR approval · no silent failure: every
red is a fail with a reason · number: merge rate.
