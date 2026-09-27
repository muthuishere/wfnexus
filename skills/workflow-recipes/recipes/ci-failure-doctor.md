# Recipe: ci-failure-doctor — one root-cause issue per CI failure

A failed CI run on the main branch is read by commands (logs, commit, open
`[ci]` issues); one agent extracts the first meaningful error and the suspect
commit; a classifier decides whether it is the same cause as an open issue; the
issue is filed or appended to. A fix is attempted only when the classifier says
it is safe, and the fix PR waits for a person.

## Use when

- "Main goes red and nobody knows why for an hour."
- "The same flaky failure gets five duplicate issues."

## Not when

- The failure is one known flaky test → `flaky-test-fixer` shape (workflow-author).
- They have no CI yet — say so.

## Evidence

Catalogue `debugging-ops` §1 `ci-failure-doctor`: CI Doctor "9 merged PRs out
of 13 proposed (69%)"; GitLab's fix-pipeline flow declines when "security-
sensitive and should be reviewed by a person". Summary: build-these-first #3.

## Questions — one at a time, offer the default

1. **Which repository and branch?** → `{{REPO}}`, `{{BRANCH}}` *(default: this project's repo, `main`)*
2. **How do we hear about a failure?** → trigger *(default: a schedule every 30
   minutes that looks at `gh run list --status failure` since the last run, plus
   `repository_dispatch: ci_failed` if they can forward the `workflow_run` webhook)*
   Toy check: "I'll run it when I notice" — the point is not noticing.
3. **Label for CI issues?** → `{{LABEL}}` *(default: `ci-failure`)*
4. **Should it attempt a fix, or diagnose only?** *(default: diagnose only for
   the first two weeks; turn on fixes once the issue precision is known)*
   → keep or delete the `fix`/`publish` steps.
5. **Success number?** *(default: duplicate CI issues → 0; minutes from red to an
   issue with a named suspect commit; if fixing, fix-PR merge rate — baseline 69%)*

## Skeleton — `<name>/workflow.yaml`

Sidecars: `sidecars/route.py`, `sidecars/file_issues.py`, `questions.yaml`.

```yaml
name: {{NAME}}
description: >-
  Reads each failed CI run on {{REPO}}@{{BRANCH}}, finds the first meaningful
  error and the suspect commit, and files or updates ONE {{LABEL}} issue per root
  cause. Success: {{SUCCESS_NUMBER}}.

on:
  workflow_dispatch:
  schedule:
    - cron: "{{CRON}}"
  repository_dispatch:
    types: [ci_failed]

input_schema:
  type: object
  properties:
    run_id:  { type: string, title: "CI run id (empty = latest failure)" }
    dry_run: { type: boolean, title: "Dry run — do not file", default: false }

steps:
  - id: fetch
    name: Fetch the failed run's logs
    run: |
      RID="{{ .Input.run_id }}"
      [ -n "$RID" ] || RID=$(gh run list -R {{REPO}} --branch {{BRANCH}} --status failure --limit 1 --json databaseId -q '.[0].databaseId')
      [ -n "$RID" ] || { echo "no failed run"; exit 0; }
      gh run view "$RID" -R {{REPO}} --log-failed | tail -c 60000 > failed.log
      gh run view "$RID" -R {{REPO}} --json headSha,displayTitle,url,conclusion > run.json
      gh issue list -R {{REPO}} --label {{LABEL}} --state open --json number,title,body > open_issues.json
      exit 3
    timeout_sec: 180
    output_schema:
      type: object
      properties:
        ok:       { type: boolean }
        exitCode: { type: integer }
        stdout:   { type: string }
        stderr:   { type: string }

  - id: read
    name: First meaningful error and suspect commit
    needs: [fetch]
    when:
      - path: fetch.exitCode
        equals: 3
    skills: [repo-navigator]
    tools: [read, grep, glob, bash]
    max_turns: 25
    system: You read CI logs for the FIRST error that explains the failure, not the loudest one.
    prompt: |
      ./failed.log, ./run.json and ./open_issues.json are here; the repository is checked out.
      Find the first meaningful error, the failing test/job, the suspect commit, and a
      category. Write findings.jsonl with ONE line:
      {"id": "<stable fingerprint: category + test/job + first error line without numbers>",
       "title": "[{{LABEL}}] <short cause>", "body": "<markdown with log excerpt and commit>",
       "state": "<the facts, plus the titles of open {{LABEL}} issues>"}
    output_schema:
      type: object
      required: [fingerprint, category, first_error, suspect_commit, same_as_issue]
      properties:
        fingerprint:    { type: string }
        category:       { type: string, enum: [test, build, infra, flaky, dependency, unknown] }
        first_error:    { type: string }
        suspect_commit: { type: string }
        same_as_issue:  { type: integer, description: "open issue number with the same root cause, 0 if none" }

  - id: verdict
    name: Real and actionable? (JEV)
    needs: [read]
    when:
      - path: fetch.exitCode
        equals: 3
    run: |
      wfx judge -q ./questions.yaml --items findings.jsonl > verdicts.jsonl || echo "judge failed"
      python3 route.py findings.jsonl verdicts.jsonl actionable
    timeout_sec: 120
    output_schema:
      type: object
      properties:
        ok:       { type: boolean }
        exitCode: { type: integer }
        stdout:   { type: string }
        stderr:   { type: string }

  - id: file
    name: File or append to the issue
    needs: [verdict]
    when:
      - path: fetch.exitCode
        equals: 3
    run: REPO={{REPO}} LABEL={{LABEL}} DRY_RUN={{ .Input.dry_run }} COMMENT_EVERY_H=0 python3 file_issues.py yes.jsonl
    timeout_sec: 120
    output_schema:
      type: object
      properties:
        ok:       { type: boolean }
        exitCode: { type: integer }
        stdout:   { type: string }
        stderr:   { type: string }

  - id: escalate
    name: Unsure — ask a person
    needs: [file]
    when:
      - path: verdict.exitCode
        equals: 3
    skills: [approval-desk]
    tools: [read, bash, ask_human]
    max_turns: 10
    prompt: |
      ./uncertain.jsonl holds a CI failure the classifier could not settle (real or noise?).
      Ask the person with ask_human; if they say file it, run
      REPO={{REPO}} LABEL={{LABEL}} DRY_RUN={{ .Input.dry_run }} python3 file_issues.py uncertain.jsonl
    output_schema:
      type: object
      required: [filed]
      properties:
        filed: { type: boolean }

  # Optional (Q4): a fix attempt only for an actionable finding the classifier
  # also called safe_to_fix = yes. Delete safety/fix/publish for diagnose-only.
  - id: safety
    name: Safe to attempt a fix? (JEV)
    needs: [file]
    when:
      - path: verdict.exitCode
        equals: 0
    run: python3 route.py yes.jsonl verdicts.jsonl safe_to_fix safe_
    timeout_sec: 30
    output_schema:
      type: object
      properties:
        ok:       { type: boolean }
        exitCode: { type: integer }
        stdout:   { type: string }
        stderr:   { type: string }

  - id: fix
    name: Draft a fix on a branch
    needs: [safety]
    when:
      - path: safety.exitCode
        equals: 0
    skills: [fix-author]
    tools: [read, write, edit, grep, glob, bash]
    max_turns: 40
    prompt: |
      Fix the CI failure described in ./safe_yes.jsonl on a new branch; run the failing
      test until it passes and the suite stays green. Never push.
    output_schema:
      type: object
      required: [branch, tests_pass]
      properties:
        branch:     { type: string }
        tests_pass: { type: boolean }
    gates:
      - field: tests_pass
        equals: false
        action: fail
        message: "The fix did not make the suite green; the issue stands, no PR."

  - id: publish
    name: Open the fix PR — a person approves
    needs: [fix]
    when:
      - path: fix.tests_pass
        equals: true
    requires_approval: true
    skills: [pr-publisher]
    tools: [read, bash]
    max_turns: 10
    prompt: Push {{ .Steps.fix.branch }} and open a PR against {{BRANCH}} that links the {{LABEL}} issue.
    output_schema:
      type: object
      required: [pr_url]
      properties:
        pr_url: { type: string }
```

## `questions.yaml`

```yaml
actionable:
  type: noul
  instructions: Is this a real failure caused by the code or its build, that someone should act on?
  "true": a deterministic test/build error tied to a change, or infrastructure that will keep failing
  "false": a cancelled run, a one-off runner outage already green on retry, or noise
safe_to_fix:
  type: noul
  instructions: Is an automatic fix attempt safe here?
  "true": a contained code or test change with a clear error
  "false": security-sensitive, a migration, secrets, or infrastructure a person must own
```

## Gates

Healthy (no failed run) → exit 0 and nothing runs. `uncertain` → escalate.
The fix PR has `requires_approval: true`; `fix` fails the run if tests stay red.
`fix` runs only for findings routed yes on BOTH actionable and safe_to_fix.

## Success number

Duplicate CI issues per month (target 0); red-to-issue minutes; fix-PR merge
rate (catalogue baseline 69%).

## Bar check

trigger: schedule/webhook · record: Actions logs + issues · output: deduped
issue (+ PR) · verify: JEV + test rerun · gate: PR approval · no silent failure:
uncertain → person · number: above.
