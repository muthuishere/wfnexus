# Recipe: issue-triage-labelling — every new issue labelled, confidence-gated

New or unlabelled issues are fetched with similar open ones; a classifier picks
a label from THEIR allow-list and whether it duplicates an open issue; high-
confidence labels are applied (internal, reversible, visible in the timeline);
uncertain ones become a suggestion comment; closing as duplicate — which the
reporter sees — waits for a person.

## Use when

- "Our issue tracker is a swamp", "nobody labels anything", "same bug reported 5 times".

## Not when

- A handful of issues a month — a person does it in a minute.
- Support tickets from customers → `inbound-ticket-triage`.

## Evidence

Catalogue `product-design` §3 `issue-intake-triage`: "the 'hello world' of
automated agentic workflows"; Copilot "applies only high-confidence changes by
default"; "All actions are visible in the issue timeline and can be undone".
No cited accuracy number — so this recipe samples its own.

## Questions — one at a time, offer the default

1. **Which repository?** → `{{REPO}}`
2. **The label allow-list, and what each MEANS?** → `{{LABELS_YAML}}` in
   `questions.yaml` *(default: read `gh label list` and propose a meaning for each;
   they correct)* Toy check: labels without meanings are guesses — ask for one line each.
3. **When?** → `{{CRON}}` *(default: hourly sweep of unlabelled open issues; add
   `repository_dispatch: issue_opened` if they forward webhooks)*
4. **Should it ask the reporter for missing repro info?** *(default: suggest the
   question in a comment — posting to the reporter is outward, so it waits for approval)*
5. **Success number?** *(default: label accuracy ≥ 90% on a weekly sample of 20;
   median time-to-label under 1 hour)*

## Skeleton — `<name>/workflow.yaml`

Sidecars: `sidecars/route.py`, `questions.yaml`.

```yaml
name: {{NAME}}
description: >-
  Labels every unlabelled issue in {{REPO}} from the allow-list, applying only
  high-confidence labels and suggesting the rest; duplicate-closing waits for a
  person. Success: {{SUCCESS_NUMBER}}.

on:
  workflow_dispatch:
  schedule:
    - cron: "{{CRON}}"
  repository_dispatch:
    types: [issue_opened]

steps:
  - id: fetch
    name: Unlabelled issues and the open ones they might duplicate
    run: |
      gh issue list -R {{REPO}} --state open --limit 200 --json number,title,body,labels > open.json
      python3 - <<'PY'
      import json, sys
      issues = json.load(open("open.json"))
      todo = [i for i in issues if not i["labels"]][:30]
      titles = "\n".join(f"#{i['number']} {i['title']}" for i in issues)
      with open("items.jsonl", "w") as f:
          for i in todo:
              f.write(json.dumps({"id": str(i["number"]), "title": i["title"],
                  "state": f"Issue #{i['number']}: {i['title']}\n\n{(i['body'] or '')[:3000]}\n\nOther open issues:\n{titles[:4000]}"}) + "\n")
      print(len(todo), "to label")
      sys.exit(3 if todo else 0)
      PY
    timeout_sec: 120
    output_schema:
      type: object
      properties:
        ok:       { type: boolean }
        exitCode: { type: integer }
        stdout:   { type: string }
        stderr:   { type: string }

  - id: verdict
    name: Label and duplicate check (JEV)
    needs: [fetch]
    when:
      - path: fetch.exitCode
        equals: 3
    run: |
      wfx judge -q ./questions.yaml --items items.jsonl > verdicts.jsonl || echo "judge failed"
      python3 route.py items.jsonl verdicts.jsonl label_confident
    timeout_sec: 300
    output_schema:
      type: object
      properties:
        ok:       { type: boolean }
        exitCode: { type: integer }
        stdout:   { type: string }
        stderr:   { type: string }

  - id: apply
    name: Apply confident labels, suggest the rest
    needs: [verdict]
    when:
      - path: fetch.exitCode
        equals: 3
    run: |
      python3 - <<'PY'
      import json, subprocess
      def rows(p): return [json.loads(l) for l in open(p) if l.strip()]
      for r in rows("yes.jsonl"):
          lab = r["answers"]["label"]["choice"]
          subprocess.run(["gh", "issue", "edit", r["id"], "-R", "{{REPO}}", "--add-label", lab], check=True)
          print("labelled", r["id"], lab)
      for r in rows("uncertain.jsonl") + rows("no.jsonl"):
          lab = (r.get("answers", {}).get("label") or {}).get("choice", "?")
          subprocess.run(["gh", "issue", "comment", r["id"], "-R", "{{REPO}}", "--body",
              f"Triage suggestion (not applied, low confidence): `{lab}`. A maintainer decides."], check=True)
          print("suggested", r["id"], lab)
      import sys
      dup = [r["id"] for r in rows("yes.jsonl") + rows("uncertain.jsonl") + rows("no.jsonl")
             if (r.get("answers", {}).get("duplicate") or {}).get("band") == "yes"]
      print("probable duplicates:", dup)
      sys.exit(5 if dup else 0)
      PY
    timeout_sec: 300
    output_schema:
      type: object
      properties:
        ok:       { type: boolean }
        exitCode: { type: integer }
        stdout:   { type: string }
        stderr:   { type: string }

  - id: duplicates
    name: Close duplicates — a person approves
    needs: [apply]
    when:
      - path: apply.exitCode
        equals: 5
    requires_approval: true
    tools: [read, bash, ask_human]
    max_turns: 15
    prompt: |
      In ./verdicts.jsonl, issues whose `duplicate` answer is in the yes band are
      probable duplicates. Show each pair to the person with ask_human; close only the
      ones they confirm, with `gh issue close <n> -R {{REPO}} --reason "not planned"
      --comment "Duplicate of #<m>"`.
    output_schema:
      type: object
      required: [closed]
      properties:
        closed: { type: array, items: { type: string } }
```

## `questions.yaml`

```yaml
label:
  type: choice
  instructions: Which one label from the allow-list fits this issue best?
  options:
    {{LABELS_YAML}}
label_confident:
  type: noul
  instructions: Is the issue clear enough that a maintainer would apply that label without discussion?
  "true": the issue plainly describes one kind of work matching one label
  "false": ambiguous, several kinds of work, or too little text to tell
duplicate:
  type: noul
  instructions: Does this issue describe the same problem as one of the other open issues listed?
  "true": same symptom and same component as a listed issue
  "false": different symptom, component, or only superficially similar words
```

`{{LABELS_YAML}}` is one `label: what it means` line per allowed label, e.g.
`bug: something that worked is broken`, indented to sit under `options:`.

## Gates

Nothing unlabelled → nothing runs. Labels apply only in the yes band; the rest
are suggestions. Closing a duplicate (reporter-visible) → `requires_approval`,
and the step only runs when `apply` exits 5 (a probable duplicate exists).

## Success number

Weekly sample of 20 labels: accuracy ≥ 90%; median time-to-label < 1 hour.

## Bar check

trigger: hourly/webhook · record: GitHub issues · output: labels + suggestion
comments · verify: JEV confidence band + weekly sample · gate: duplicate-close
approval · no silent failure: missing verdict → suggestion, not a label · number: accuracy.
