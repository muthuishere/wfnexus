# Recipe: alert-triage — explain the page, allow "inconclusive"

When an alert fires, commands pull the alert window, recent deploys and merged
changes; ONE read-only agent ranks hypotheses with evidence; a second pass tries
to refute them; a classifier scores evidence strength; the note goes on the
incident only if the evidence is strong, and any mitigation waits for a person.

## Use when

- "When the pager goes off I spend 20 minutes finding what changed."
- "Explain our Alertmanager / PagerDuty / Grafana alerts before I open the laptop."

## Not when

- Nothing alerts yet — start with `production-watch`, which raises the signal.
- They want the agent to restart/roll back by itself — refuse; that is the gate.

## Evidence

Catalogue `debugging-ops` §4 `alert-investigator`: Meta "42% of these
investigations had the root cause in the top five"; Relvy on OpenRCA 36% → 48%
with a harness; "Very few are ready to give AI write access to production."

## Questions — one at a time, offer the default

1. **Which alerts?** → `{{ALERT_FILTER}}` *(default: severity p1/p2 only — a
   p4 does not deserve an investigation)*
2. **How does the alert reach us?** → trigger. *(default:
   `repository_dispatch: alert_fired`, posted by an Alertmanager/PagerDuty
   webhook forwarder; plus `workflow_dispatch` with `alert_id` for a manual run)*
   Toy check: "I'll paste the alert in" is not a trigger — it is a chat.
3. **Where are the logs and metrics for the alert window?** Read-only commands.
   → `{{EVIDENCE_COMMAND}}` *(default: `logcli query` for Loki over the window
   and `curl $PROM_URL/api/v1/query_range` for the alerting expression)*
4. **Where is deploy history?** → `{{DEPLOYS_COMMAND}}` *(default:
   `gh run list -R <repo> --workflow deploy --created ">=$(date -u -v-24H +%F)"` and
   `git log --since=24.hours --merges`)*
5. **Where does the finding go?** → `{{NOTE_COMMAND}}` *(default: a comment on
   the incident's GitHub issue via `gh issue comment`; Slack needs a webhook NAME)*
6. **What mitigations exist, and who approves them?** → the `mitigate` step's
   approver *(default: nothing automatic — a proposed rollback/restart waits for
   the on-call's approval, and the command they approve is shown verbatim)*
7. **Success number?** *(default: root cause in the top hypothesis, graded by the
   on-call after the incident; baseline: minutes-to-first-hypothesis today)*

## Skeleton — `<name>/workflow.yaml`

Sidecars: `sidecars/route.py`, `questions.yaml`.

```yaml
name: {{NAME}}
description: >-
  Investigates {{ALERT_FILTER}} alerts read-only: pulls the alert window, recent
  deploys and merges, ranks hypotheses with evidence, tries to refute each, and
  posts a finding or "inconclusive". Any mitigation waits for the on-call.
  Success: {{SUCCESS_NUMBER}}.

on:
  workflow_dispatch:
  repository_dispatch:
    types: [alert_fired]

input_schema:
  type: object
  required: [alert_id]
  properties:
    alert_id:   { type: string, title: "Alert / incident id" }
    alert_text: { type: string, title: "Alert payload", format: textarea }
    window_min: { type: integer, title: "Look back (minutes)", default: 60 }

steps:
  - id: gather
    name: Gather the window, deploys and merges
    run: |
      cat > alert.txt <<'ALERT_EOF'
      {{ .Input.alert_text }}
      ALERT_EOF
      {
        echo "## alert"; cat alert.txt
        echo "## evidence"; {{EVIDENCE_COMMAND}}
        echo "## deploys and merges"; {{DEPLOYS_COMMAND}}
      } > evidence.md 2>&1
      test -s evidence.md
    timeout_sec: 180
    output_schema:
      type: object
      properties:
        ok:       { type: boolean }
        exitCode: { type: integer }
        stdout:   { type: string }
        stderr:   { type: string }

  - id: investigate
    name: Rank hypotheses (read-only)
    needs: [gather]
    skills: [prod-signal-triage]
    tools: [read, grep, glob]
    max_turns: 30
    system: >-
      You investigate; you never change anything. Every claim links to a line of
      evidence.md or a file:line. "inconclusive" is a correct answer.
    prompt: |
      Alert {{ .Input.alert_id }}. ./evidence.md holds the window, deploys and merges.
      Rank up to 3 hypotheses for the cause, each with the evidence lines that support
      it and what would disprove it. Write hypotheses.jsonl — one line each:
      {"id": "h1", "state": "<hypothesis + its evidence, plain sentences>"}.
    output_schema:
      type: object
      required: [hypotheses, conclusion]
      properties:
        conclusion: { type: string, enum: [likely_cause_found, inconclusive] }
        hypotheses:
          type: array
          items:
            type: object
            required: [id, cause, evidence, disproved_by]
            properties:
              id:           { type: string }
              cause:        { type: string }
              evidence:     { type: array, items: { type: string } }
              disproved_by: { type: string }
              mitigation:   { type: string }

  - id: refute
    name: Try to refute each hypothesis
    needs: [investigate]
    tools: [read, grep, glob]
    max_turns: 20
    system: You are the sceptic. You look for the evidence that makes each hypothesis wrong.
    prompt: |
      Hypotheses: {{ json .Steps.investigate.hypotheses }}
      For each, check ./evidence.md and the code for what would disprove it.
      Rewrite hypotheses.jsonl, appending to each "state" what you found for and against.
    output_schema:
      type: object
      required: [surviving]
      properties:
        surviving: { type: array, items: { type: string } }
        refuted:   { type: array, items: { type: string } }

  - id: verdict
    name: Score evidence strength (JEV)
    needs: [refute]
    run: |
      wfx judge -q ./questions.yaml --items hypotheses.jsonl > verdicts.jsonl || echo "judge failed"
      python3 route.py hypotheses.jsonl verdicts.jsonl supported
    timeout_sec: 120
    output_schema:
      type: object
      properties:
        ok:       { type: boolean }
        exitCode: { type: integer }
        stdout:   { type: string }
        stderr:   { type: string }

  - id: post
    name: Post the finding (or "inconclusive")
    needs: [verdict]
    run: |
      python3 -c "
      import json
      yes=[json.loads(l) for l in open('yes.jsonl') if l.strip()]
      print('## Alert {{ .Input.alert_id }} — ' + ('likely cause' if yes else 'inconclusive'))
      for h in yes: print('-', h['state'])
      " > note.md
      {{NOTE_COMMAND}} note.md
    timeout_sec: 60
    output_schema:
      type: object
      properties:
        ok:       { type: boolean }
        exitCode: { type: integer }
        stdout:   { type: string }
        stderr:   { type: string }

  - id: mitigate
    name: Propose a mitigation — the on-call approves
    needs: [post]
    when:
      - path: investigate.conclusion
        equals: likely_cause_found
    requires_approval: true
    tools: [read, ask_human]
    max_turns: 10
    prompt: |
      Surviving hypotheses: {{ json .Steps.refute.surviving }}.
      Ask the on-call, with ask_human, whether to apply a mitigation, showing the
      exact command. Record their answer. You do not run it.
    output_schema:
      type: object
      required: [decision]
      properties:
        decision: { type: string, enum: [apply, hold, not_needed] }
        command:  { type: string }
```

## `questions.yaml`

```yaml
supported:
  type: noul
  instructions: Does the cited evidence establish this hypothesis as the cause of the alert?
  "true": the evidence lines directly show the mechanism and its timing matches the alert
  "false": the evidence is circumstantial, contradicts it, or the timing does not match
```

## Gates

`uncertain`/`no` hypotheses are posted as "inconclusive", never as a cause.
`mitigate` has `requires_approval: true`; the agent has no bash.

## Success number

Top-hypothesis accuracy graded by the on-call; minutes from page to first
evidence-backed hypothesis (baseline: today's).

## Bar check

trigger: alert webhook · record: logs/metrics/deploys · output: typed hypotheses
+ note · verify: refutation pass + JEV · gate: mitigation approval · no silent
failure: missing verdict = uncertain = inconclusive · number: above.
