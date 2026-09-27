# Recipe: production-watch — know before a customer does

Logs, metrics and database tables are read on a schedule by plain commands;
anything past a threshold is explained against the code by ONE agent, judged
by a calibrated classifier, and filed as ONE issue per problem (deduplicated by
fingerprint). Anything the classifier is unsure about stops and asks a person.

## Use when

- "I want to know when payments / signups / jobs / the queue start failing."
- "Tell me when errors spike", "watch production", "the cron silently died last month".

## Not when

- Something already pages them for exactly this (PagerDuty/Alertmanager) — use
  `alert-triage` to explain the page instead of raising a second one.
- They want to FIX the error automatically — that is `bug-fixer` after this
  files the issue; chain it later, do not merge the two.

## Evidence (who does this, and how well)

Catalogue `debugging-ops` §2 `error-to-fix-pr` (Sentry Seer; dotnet "Bug Fix
317 PRs 69.4%" merged) and §4 `alert-investigator` (Meta: "42% of these
investigations had the root cause in the top five"). The house example is
reqsume-prod-watch (`reqsume-wfx/.wfx/workflows/reqsume-prod-watch/`), which
filed issues #504–#507 from a real run. Summary pattern 1: deterministic
around, inference inside — the rules raise a candidate, the agent may not
invent one.

## Questions — one at a time, offer the default

1. **What exactly counts as "failing"?** Name the thing and the number.
   → one or more `signals.yaml` entries (name, title, op, threshold, severity).
   *(default for payments: more than 5 failed payments in 15 minutes, OR the
   failure rate over 15 minutes above 10%, OR zero successful payments in 30
   minutes during business hours)*
   Toy check: "tell me if anything looks off" is not a rule — ask for a number.
2. **Where does that number live — which database, log store or API?** Get the
   read-only command that prints it. → each signal's `command`.
   *(default: a read-only SQL count via `psql "$DB_URL" -Atc "…"`; Loki
   `logcli`; Prometheus `curl …/api/v1/query`)*
   Toy check: a CSV export, a staging DB or a spreadsheet copy is not the system
   of record — ask for the production read replica or a read-only role.
3. **Which credential NAME reaches it?** → `{{SECRET_ENV_NAMES}}` (check with
   `wfx env --project <p>`; never ask for the value). *(default: `DB_URL`)*
4. **How often, and how far back does each look?** → `{{CRON}}`,
   `{{WINDOW_MIN}}`. *(default: every 15 minutes, 20-minute window so runs overlap)*
5. **Which repository is the code that handles it?** → the project the
   workflow is installed in (the triage agent reads that checkout).
   *(default: the current project)*
6. **Where should a confirmed problem go?** → `{{ISSUE_REPO}}`, `{{ISSUE_LABEL}}`.
   *(default: this repo's GitHub issues, label `sre-alert`)*
   Toy check: "just print it" / "send me a summary" is not a system of record.
7. **Who decides when the classifier is unsure?** → the `escalate` step asks
   them in the run (and on the channels the platform notifies). *(default: you,
   through the approval desk)*
8. **What number tells us this is working?** → `{{SUCCESS_NUMBER}}` in the
   description. *(default: problems caught before a customer reported them, and
   precision of filed issues ≥ 80% — share of filed issues not closed as noise)*

## Skeleton — `<name>/workflow.yaml`

Sidecars to copy beside it: `sidecars/watch.py`, `sidecars/route.py`,
`sidecars/file_issues.py`, plus `signals.yaml` (from Q1–Q3) and
`questions.yaml` (below).

```yaml
name: {{NAME}}
description: >-
  Watches {{WHAT}} every {{EVERY_HUMAN}}: {{SIGNALS_HUMAN}}. A breached rule is
  explained against the code, judged by a calibrated classifier and filed as ONE
  {{ISSUE_LABEL}} issue per problem in {{ISSUE_REPO}}; anything the classifier is
  unsure of stops and asks a person. Success: {{SUCCESS_NUMBER}}.

on:
  workflow_dispatch:
  schedule:
    - cron: "{{CRON}}"

input_schema:
  type: object
  properties:
    dry_run: { type: boolean, title: "Dry run — report, do not file", default: false }

steps:
  # 1. Every signal, read-only, by rule. Exit 0 = healthy and nothing else runs.
  - id: collect
    name: Read the signals
    description: Runs each read-only check in signals.yaml; a check that cannot run is a finding.
    run: python3 watch.py signals.yaml
    timeout_sec: 180
    output_schema:
      type: object
      properties:
        ok:       { type: boolean }
        exitCode: { type: integer }
        stdout:   { type: string }
        stderr:   { type: string }

  # 2. The one agent: explain each candidate against the code. It may not add one.
  - id: triage
    name: Explain each breach against the code
    description: Probable cause with file:line, impact, smallest fix — or "unknown".
    needs: [collect]
    when:
      - path: collect.ok
        equals: false
    skills: [prod-signal-triage]
    tools: [read, grep, glob, bash]
    max_turns: 30
    system: >-
      You are the on-call engineer reading signals you did not produce. You
      report only what the evidence and the code show, and you say "unknown"
      rather than guess a cause.
    prompt: |
      ./signals.json holds the breached rules for {{WHAT}}. The repository here is
      the code that handles it. For EVERY entry in `candidates`, and only those:
      explain what is wrong and the impact on customers or money, find the probable
      cause (cite file:line or quote the evidence line), and the smallest fix.
      A `source_broken` candidate means the check itself could not run — say why.

      Then write findings.jsonl — one line per candidate:
      {"id": <fingerprint unchanged>, "title": "[{{ISSUE_LABEL}}] <title>",
       "body": "<markdown: what, impact, evidence, cause, fix>",
       "state": "<the same facts as plain sentences, for the classifier>"}
    output_schema:
      type: object
      required: [findings]
      properties:
        findings:
          type: array
          items:
            type: object
            required: [fingerprint, title, severity, impact, evidence, probable_cause, suggested_fix]
            properties:
              fingerprint:    { type: string }
              title:          { type: string }
              severity:       { type: string, enum: [critical, error, warning] }
              impact:         { type: string }
              evidence:       { type: array, items: { type: string } }
              probable_cause: { type: string }
              code_refs:      { type: array, items: { type: string } }
              suggested_fix:  { type: string }

  # 3. Calibrated verdict per finding; yes / uncertain / no. Missing verdict = uncertain.
  - id: verdict
    name: Judge each finding (JEV)
    description: actionable? per finding on a calibrated classifier, then routed by band.
    needs: [triage]
    when:
      - path: collect.ok
        equals: false
    run: |
      test -s findings.jsonl || { echo "triage wrote no findings.jsonl"; exit 2; }
      wfx judge -q ./questions.yaml --items findings.jsonl > verdicts.jsonl || echo "judge failed — every finding goes to a person"
      python3 route.py findings.jsonl verdicts.jsonl actionable
    timeout_sec: 180
    output_schema:
      type: object
      properties:
        ok:       { type: boolean }
        exitCode: { type: integer }
        stdout:   { type: string }
        stderr:   { type: string }

  # 4. File the yes ones — internal, reversible, deduplicated.
  - id: file
    name: File confirmed problems
    description: One issue per fingerprint; an open one gets a comment at most daily.
    needs: [verdict]
    when:
      - path: collect.ok
        equals: false
    run: REPO={{ISSUE_REPO}} LABEL={{ISSUE_LABEL}} DRY_RUN={{ .Input.dry_run }} python3 file_issues.py yes.jsonl
    timeout_sec: 120
    output_schema:
      type: object
      properties:
        ok:       { type: boolean }
        exitCode: { type: integer }
        stdout:   { type: string }
        stderr:   { type: string }

  # 5. Only when the classifier was unsure (or failed): a person decides.
  - id: escalate
    name: Ask a person about the uncertain ones
    description: Shows what the classifier could not settle; files only what the person names.
    needs: [file]
    when:
      - path: verdict.exitCode
        equals: 3
    skills: [approval-desk]
    tools: [read, bash, ask_human]
    max_turns: 15
    prompt: |
      ./uncertain.jsonl lists findings the classifier could not settle. Ask the
      person, with ask_human, which to file — show each one's title, one-line
      evidence and its actionable number, numbered. Then run, for ONLY those:
        REPO={{ISSUE_REPO}} LABEL={{ISSUE_LABEL}} DRY_RUN={{ .Input.dry_run }} python3 file_issues.py uncertain.jsonl --only <id1,id2>
    output_schema:
      type: object
      required: [filed, skipped]
      properties:
        filed:   { type: array, items: { type: string } }
        skipped: { type: array, items: { type: string } }
```

## `signals.yaml` — one entry per rule from Q1/Q2

```yaml
- name: {{SIGNAL_ID}}
  title: "{{SIGNAL_TITLE}}"
  command: {{READ_ONLY_COMMAND_PRINTING_ONE_NUMBER}}
  op: "{{OP}}"
  threshold: {{THRESHOLD}}
  severity: {{SEVERITY}}
```

## `questions.yaml` — the JEV gate

```yaml
# Bands: no < 0.30 < uncertain < 0.70 < yes. yes -> filed; uncertain -> a person; no -> logged only.
actionable:
  type: noul
  instructions: >-
    Given the evidence, is this a real, current problem with {{WHAT}} that an
    engineer should act on?
  "true": >-
    The numbers show {{WHAT}} failing or about to — customers affected, money or
    data at risk, or monitoring blind because a check itself cannot run.
  "false": >-
    Expected behaviour (a declined card, a retry that succeeded, a scheduled
    maintenance window), a one-off blip already recovered, or noise.
urgency:
  type: choice
  instructions: How soon must a person act on this?
  options:
    now: customers or money are affected right now
    soon: degrading or blind — act this week
    later: real but harmless for now
```

## Gates

- `collect` exit 0 → every later step is skipped (healthy costs no model).
- `route.py` exit 3 → `escalate` asks a person; a missing verdict counts as uncertain.
- Filing an internal issue in their own repo is the only automatic write, and
  only in the `yes` band. Anything outward (status page, customer email, a
  rollback) is NOT in this recipe — add a `requires_approval: true` step if asked.

## Success number

Caught-before-customer count per month, and precision of filed issues (target
≥ 80% not closed as noise). Baseline: how many of last month's incidents a
customer reported first.

## Bar check

trigger: cron · record: production DB/logs read-only · output: deduped issues
with schema · verify: rules + agent + JEV · gate: uncertain → person; nothing
outward · no silent failure: broken check = finding, missing verdict = uncertain
· number: above.
