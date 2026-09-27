# Recipe: weekly-kpi-narrative — the Monday numbers, reconciled and explained

Monday morning: commands pull each KPI from its source and the same metric from
a second source; a reconciliation flags disagreements; a classifier decides
which moves and mismatches are material; one agent writes the narrative for
material moves only, each figure linked to its query; the owner approves before
it goes to leadership.

## Use when

- "Someone spends Monday morning building the KPI deck."
- "Billing and the dashboard disagree and we find out in the board meeting."

## Not when

- They only want a chart — a BI dashboard already does that; the value here is
  the reconciliation and the explained moves.

## Evidence

Catalogue `data` §3 `weekly-kpi-narrative`: Poshmark weekly reports "saving
hours of manual work every week"; agency reporting "22 hours to 2 hours per
month" (integrator claim); Tag "spots a vendor report that disagrees with the
numbers, and flags it before moving on" (2026-09-02).

## Questions — one at a time, offer the default

1. **Which KPIs, and the query for each?** → `kpis.yaml` entries (name, command
   printing one number for last week, command for the week before) *(default: MRR,
   new customers, churned customers, failed payments — from the production read replica)*
   Toy check: numbers typed into a sheet by hand are not a source.
2. **Which KPI has a second source to reconcile against?** → `reconcile` command
   *(default: MRR from billing (Stripe) vs MRR from the warehouse)*
3. **What move is material?** → `{{MATERIAL_PCT}}` *(default: ±10% week over week, or any reconciliation gap > 2%)*
4. **Who approves, and where does it go?** → `{{PUBLISH_CMD}}` *(default: the
   owner approves; then posted to the leadership Slack channel via a webhook NAME,
   or saved as a Google Doc)*
5. **Schedule?** → `{{CRON}}` *(default: Monday 07:00 UTC)*
6. **Success number?** *(default: hours spent on the weekly report → under 0.5;
   mismatches caught before the meeting)*

## Skeleton — `<name>/workflow.yaml`

Sidecars: `sidecars/route.py`, `sidecars/kpis.py`, `questions.yaml`, `kpis.yaml` (below).

```yaml
name: {{NAME}}
description: >-
  Every Monday: pulls each KPI and its week-before value, reconciles against a
  second source, explains only material moves (±{{MATERIAL_PCT}}%) with every
  figure linked to its query, and posts after the owner approves.
  Success: {{SUCCESS_NUMBER}}.

on:
  workflow_dispatch:
  schedule:
    - cron: "{{CRON}}"

steps:
  - id: pull
    name: Pull and reconcile
    run: python3 kpis.py kpis.yaml {{MATERIAL_PCT}}
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
        message: "A KPI source could not be read — no report on partial numbers."

  - id: verdict
    name: Material? (JEV)
    needs: [pull]
    run: |
      wfx judge -q ./questions.yaml --items moves.jsonl > verdicts.jsonl || echo "judge failed"
      python3 route.py moves.jsonl verdicts.jsonl material
    timeout_sec: 180
    output_schema:
      type: object
      properties:
        ok:       { type: boolean }
        exitCode: { type: integer }
        stdout:   { type: string }
        stderr:   { type: string }

  - id: narrate
    name: Explain the material moves
    needs: [verdict]
    tools: [read, write]
    max_turns: 20
    prompt: |
      ./kpis.json has every KPI with this week, last week, delta and the query that produced
      it; ./yes.jsonl the material moves and mismatches; ./uncertain.jsonl the ones to list
      as "may matter". Write report.md: the table, then one short paragraph per material
      move, each figure followed by its query name. Flag every reconciliation mismatch
      at the TOP. Never state a cause the data does not show — say "cause unknown".
    output_schema:
      type: object
      required: [material_moves, mismatches]
      properties:
        material_moves: { type: integer }
        mismatches:     { type: integer }

  - id: publish
    name: Post — the owner approves
    needs: [narrate]
    requires_approval: true
    run: "{{PUBLISH_CMD}} report.md"
    timeout_sec: 60
    output_schema:
      type: object
      properties:
        ok:       { type: boolean }
        exitCode: { type: integer }
        stdout:   { type: string }
        stderr:   { type: string }
```

## `kpis.yaml`

```yaml
- name: {{KPI_ID}}
  this_week: {{READ_ONLY_COMMAND_LAST_7_DAYS}}
  last_week: {{READ_ONLY_COMMAND_PREVIOUS_7_DAYS}}
  reconcile: {{SECOND_SOURCE_COMMAND_OR_EMPTY}}
```

`sidecars/kpis.py` runs them (a failed command exits 1 — no report on partial
numbers), writes `kpis.json` and `moves.jsonl` (only moves/gaps past the
threshold, in the `{id, state}` shape `wfx judge` reads). Deterministic, no model.

## `questions.yaml`

```yaml
material:
  type: noul
  instructions: Would leadership need to know about this move or source disagreement this week?
  "true": the move is beyond normal weekly variation for this KPI, or two sources disagree on the same number
  "false": seasonal/normal variation, a known one-off already explained, or rounding
```

## Gates

A source unreadable → fail (never a report on partial numbers). Distribution
to leadership → `requires_approval`. Uncertain moves are listed as "may matter",
never explained as fact.

## Success number

Hours spent on the weekly report (→ < 0.5); mismatches caught before the meeting.

## Bar check

trigger: Monday cron · record: billing + warehouse · output: report.md with
table · verify: reconciliation + JEV · gate: owner approves posting · no silent
failure: unreadable source fails the run · number: hours saved.
