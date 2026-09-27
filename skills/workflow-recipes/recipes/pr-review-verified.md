# Recipe: pr-review-verified — review findings that are checked before anyone sees them

On each new PR: commands fetch the diff and CI state; a reviewer agent writes
findings with file:line; a SECOND agent tries to refute each finding (reproduce,
read the surrounding code, run the test); a classifier keeps only findings that
survived with evidence; a person approves before the review is posted on the PR.

## Use when

- "AI review comments are 80% noise, so nobody reads them."
- "We want a second reviewer on every PR, but only real findings."

## Not when

- They want auto-approve/auto-merge — refuse; the review is advice, the merge is a person's.

## Evidence

No single catalogue entry; it is the catalogue's Summary pattern 3 ("Draft,
verify, then a named human signs") applied to review: Amazon reviews
"pass/block/investigator", Nx "highly confident AND explicitly verified",
`development` §6 test-gap-author (proof by a test that fails). Skills
`pr-reviewer` and `change-reviewer` already exist.

## Questions — one at a time, offer the default

1. **Which repository?** → `{{REPO}}` *(default: this project's)*
2. **Which PRs?** → trigger *(default: `repository_dispatch: pull_request_opened`
   from a webhook forwarder, plus `workflow_dispatch` with `pr`; skip drafts and bots)*
3. **What matters most in review here?** → `{{FOCUS}}` *(default: correctness,
   regressions, missing tests, security; NOT style — a linter does that)*
   Toy check: "everything" makes noise; pick at most four.
4. **Test command for reproducing a finding?** → `{{TEST_CMD}}` (from the repo).
5. **Who approves posting?** *(default: the PR's requested reviewer, through the approval desk)*
6. **Success number?** *(default: share of posted findings the author acts on ≥ 60%;
   false-positive rate reported by authors < 20%)*

## Skeleton — `<name>/workflow.yaml`

Sidecars: `sidecars/route.py`, `questions.yaml`.

```yaml
name: {{NAME}}
description: >-
  Reviews each PR on {{REPO}} for {{FOCUS}}; every finding is attacked by a
  second agent and scored by a classifier, and only verified findings are posted
  — after a person approves. Success: {{SUCCESS_NUMBER}}.

on:
  workflow_dispatch:
  repository_dispatch:
    types: [pull_request_opened]

input_schema:
  type: object
  required: [pr]
  properties:
    pr: { type: integer, title: "PR number" }

steps:
  - id: fetch
    name: Diff, description and CI state
    run: |
      gh pr view {{ .Input.pr }} -R {{REPO}} --json title,body,author,isDraft,headRefName,files > pr.json
      gh pr diff {{ .Input.pr }} -R {{REPO}} > pr.diff
      gh pr checkout {{ .Input.pr }} -R {{REPO}}
      python3 -c "import json,sys; sys.exit(4 if json.load(open('pr.json'))['isDraft'] else 0)"
    timeout_sec: 180
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
        message: "Draft PR or the PR could not be fetched — not reviewing."

  - id: review
    name: Review the diff
    needs: [fetch]
    skills: [pr-reviewer]
    tools: [read, grep, glob, bash]
    max_turns: 40
    prompt: |
      ./pr.diff is PR #{{ .Input.pr }}; the branch is checked out. Review for: {{FOCUS}}.
      Every finding cites file:line and says what input or path breaks. No style nits.
    output_schema:
      type: object
      required: [findings]
      properties:
        findings:
          type: array
          items:
            type: object
            required: [id, file_line, claim, why]
            properties:
              id:        { type: string }
              file_line: { type: string }
              claim:     { type: string }
              why:       { type: string }

  - id: verify
    name: Try to refute every finding
    needs: [review]
    skills: [change-reviewer]
    tools: [read, grep, glob, bash]
    max_turns: 40
    system: You are the author's advocate. A finding survives only if you cannot knock it down.
    prompt: |
      Findings: {{ json .Steps.review.findings }}
      For each: read the surrounding code, run `{{TEST_CMD}}` or a small script where it
      would prove or disprove it. Write findings.jsonl — {"id", "file_line", "claim",
      "state": "<claim, reviewer's reason, what you tried, what happened>"}.
    output_schema:
      type: object
      required: [checked]
      properties:
        checked:    { type: integer }
        reproduced: { type: array, items: { type: string } }

  - id: verdict
    name: Is each finding real? (JEV)
    needs: [verify]
    run: |
      wfx judge -q ./questions.yaml --items findings.jsonl > verdicts.jsonl || echo "judge failed"
      python3 route.py findings.jsonl verdicts.jsonl real_defect
    timeout_sec: 180
    output_schema:
      type: object
      properties:
        ok:       { type: boolean }
        exitCode: { type: integer }
        stdout:   { type: string }
        stderr:   { type: string }

  - id: post
    name: Post the verified review — a person approves
    needs: [verdict]
    when:
      - path: verdict.exitCode
        equals: 0
    requires_approval: true
    tools: [read, bash]
    max_turns: 10
    prompt: |
      Post ONE review comment on PR #{{ .Input.pr }} in {{REPO}} with the findings in
      ./yes.jsonl (file:line, claim, how it was verified), via
      `gh pr review {{ .Input.pr }} -R {{REPO}} --comment --body-file review.md`.
      Mention the count in ./uncertain.jsonl as "not verified, not posted".
    output_schema:
      type: object
      required: [posted]
      properties:
        posted: { type: integer }
```

## `questions.yaml`

```yaml
real_defect:
  type: noul
  instructions: After the refutation attempt, is this finding a real defect in the PR's change?
  "true": the verifier reproduced it or could not knock it down, and it breaks a real input or path
  "false": refuted, speculative, style-only, or about code the PR did not change
severity:
  type: choice
  instructions: How bad is it if merged?
  options:
    blocker: wrong results, data loss or a security hole
    should_fix: a real bug on an uncommon path or a missing test for new behaviour
    nit: harmless
```

## Gates

Draft/unfetchable → fail (with reason). Nothing verified → nothing posted.
Posting → `requires_approval`. Uncertain findings are never posted as findings.

## Success number

Acted-on rate of posted findings (≥ 60%); author-reported false positives (< 20%).

## Bar check

trigger: PR webhook · record: GitHub PR · output: one review · verify:
refutation agent + test run + JEV · gate: approval before posting · no silent
failure: missing verdict = not posted, counted · number: acted-on rate.
