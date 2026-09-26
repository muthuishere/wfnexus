# Top 6 workflows per category — the wfnexus template shortlist

Selected 2026-09-26 from ten research files plus Boris Cherny's notes (every number and URL below comes from them; where a file had no number, the entry says "no cited number"). Categories and ids match `apps/api/internal/workflow/categories.go`.

## Summary

**Three patterns every winning workflow shares**
1. **Deterministic around, inference inside.** `run:` steps collect the facts and check the result (tests, scanner rescans, citation lookups, `terraform plan`, three-way match); the `prompt:` step only does the fuzzy middle. Unit21, Duco, Hiflylabs, Snyk and Ronacher all converge on this, and Gartner's advice is "AI agents when decisions are needed, automation for routine workflows".
2. **A calibrated three-band gate decides who acts.** JEV `yes` → proceed, `uncertain` → `needs_input` to a human, `no` → skip or fail. Every mature product ships this shape (Copilot "Cautious: only high-confidence changes are applied", Nx "highly confident AND explicitly verified", Amazon reviews pass/block/investigator, Solventum code confidence). The band may auto-*approve* or *route*; it never auto-*denies* a person.
3. **Draft, verify, then a named human signs the outward act.** Output is a draft PR, a Gmail draft, a paused campaign or a staged journal entry, with every claim linked to its source and checked by something other than its author (test rerun, critic pass, database lookup). "Nothing merges without a human signature" (Seer); agents "do not … post to a ledger" (Anthropic FS).

**Build these six first (value × ready today)**
1. `code-remover` (development) — cleanup PRs merge at 84.7%, the highest of any agent task type in dotnet/runtime; all six skills already exist.
2. `docs-drift-fixer` (development) — 57/59 merged (96%), the best merge rate in GitHub's 298-workflow factory; needs only git + gh.
3. `ci-failure-doctor` (debugging-ops) — 9/13 merged (69%); `workflow_run` failure is a free trigger and the output is one deduplicated issue.
4. `dep-upgrade-repair` (development) — 67.4% merged in dotnet; `dep-upgrader` exists and "bump, fix, prove across languages" is a named market gap.
5. `error-to-fix-pr` (debugging-ops) — bug-fix PRs merge at 69.4%; it is the shipped `bug-fixer` template wired to a Sentry webhook with Seer-style stopping points.
6. `brief-citation-check` (legal) — 2,079 court decisions on fabricated citations; it is a deterministic lookup plus one sign-off gate, the cleanest non-dev proof of the platform.

**How to read an entry.** Step tags: `[run]` shell, `[prompt]` agent loop with an output schema, `[judge]` JEV classifier (noul bands: no <0.30 / uncertain / yes >0.70), `[approval]` `requires_approval: true`. "Ready today" means it runs on this platform now with claude-cli/codex-cli and tools that exist; otherwise it names what is missing.

---
## development

### 1. `code-remover` — Evidence-based dead-code removal PR
- **Job:** Finds code nobody calls, proves it is unused, deletes it one commit at a time and writes a PR a reviewer can check line by line.
- **Trigger:** `schedule: weekly Mon 06:00`, plus `workflow_dispatch` with a path scope.
- **Systems:** reads the repo, build and test commands; writes a branch and a GitHub PR.
- **Steps:**
  1. [run] baseline build + test counts; skip if a `[dead-code]` PR is already open.
  2. [prompt] `dead-code-finder` inventories candidates with tool output (deadcode/knip/vulture) as evidence.
  3. [prompt] `usage-prover` searches the whole repo, docs, templates and config for any use (dotnet: the agent "constrain[ed] its searches to only a portion of the repo").
  4. [judge] per candidate, noul "Is this symbol provably unused?" — yes → remove, uncertain → needs_input, no → keep.
  5. [prompt] `safe-remover` deletes one candidate per commit, with its orphaned tests; [run] build + tests after each commit.
  6. [prompt] `cleanup-pr-writer` writes the PR; [judge] "Is every claim in this PR supported by the evidence?"
  7. [approval] → [prompt] `pr-publisher` opens the PR.
- **Human gate:** approve publishing the PR (and later the merge). Deletion is the irreversible step, so the person sees the evidence table first.
- **Verify:** build and test counts before vs after each commit; the PR-claims check against evidence.
- **Success number:** PR merge rate. dotnet/runtime: "Removal/Cleanup 77 PRs 84.7%… Our highest success rate by category"; revert rate 0.6%.
- **Evidence:** https://devblogs.microsoft.com/dotnet/ten-months-with-cca-in-dotnet-runtime/ · https://github.com/github/gh-aw/tree/main/.github/workflows (dead-code-remover)
- **Skills:** dead-code-finder, usage-prover, safe-remover, cleanup-pr-writer, change-reviewer, pr-publisher.
- **Ready today?** Yes.

### 2. `docs-drift-fixer` — Keep docs true to the code
- **Job:** After code lands, finds docs that no longer match it and opens a PR fixing them.
- **Trigger:** `repository_dispatch: push.main` (paths `src/**`, `api/**`) and `schedule: daily 05:00`.
- **Systems:** reads the merged diff and docs tree; writes a docs PR.
- **Steps:**
  1. [run] list files changed since the last run (state) and the docs that reference them.
  2. [prompt] `doc-syncer` drafts edits for each stale section, citing the code line that changed.
  3. [run] docs build + link check + any README example that can execute.
  4. [judge] per edit, noul "Does this doc edit match the code at the cited line?"
  5. [approval] → [prompt] `pr-publisher`; PR auto-closes after 3 days if unmerged.
- **Human gate:** merge of the docs PR; wrong docs mislead users, so it is never pushed to main.
- **Verify:** docs build, link checker, executed examples, per-edit JEV check against source.
- **Success number:** merge rate. GitHub's factory: "Daily Documentation Updater… 57 merged PRs out of 59 proposed (96% merge rate)"; dotnet Documentation 68.1%.
- **Evidence:** https://github.github.com/gh-aw/blog/2026-01-12-welcome-to-pelis-agent-factory/ · https://www.mintlify.com/docs/agent
- **Skills:** repo-navigator, doc-syncer (new), change-reviewer, pr-publisher.
- **Ready today?** Yes.

### 3. `issue-plan-to-pr` — Labelled issue → approved plan → small PR
- **Job:** Turns a well-scoped issue into a plan a human approves, then into a tested PR.
- **Trigger:** `repository_dispatch: issues.labeled[agent-ready]` or a `/plan` comment.
- **Systems:** reads GitHub issue and repo; writes a plan comment, a branch, a PR.
- **Steps:**
  1. [judge] noul "Is this issue specific enough to act on?" — uncertain → ask_human on the issue.
  2. [prompt] `plan-writer` produces a plan with files, tests and a one-line verification.
  3. [approval] the plan (Boris: "A good plan is really important!").
  4. [prompt] `fix-author` implements; `test-author` adds the failing-then-passing test.
  5. [run] full test suite; [prompt] `change-reviewer` reviews the diff against the plan ("gaps, not style").
  6. [judge] "Does the diff stay inside the approved plan?" → [prompt] `pr-publisher`.
- **Human gate:** plan approval before code, then PR review; attention goes to the highest-leverage artifact.
- **Verify:** red/green test, full suite, fresh-context review of diff vs plan.
- **Success number:** merge rate. "Plan Command has contributed 514 merged PRs out of 761 proposed (67%)"; dotnet sweet spot "1-50 lines at 76-80%".
- **Evidence:** https://github.github.com/gh-aw/blog/2026-01-12-welcome-to-pelis-agent-factory/ · https://code.claude.com/docs/en/best-practices
- **Skills:** validate-bug, repo-navigator, plan-writer (new), fix-author, test-author, change-reviewer, pr-publisher.
- **Ready today?** Yes (needs a GitHub webhook forwarded to `repository_dispatch`).

### 4. `dep-upgrade-repair` — Bump a dependency and fix what breaks
- **Job:** Bundles pending dependency updates, repairs the breakage, and proves it with the suite.
- **Trigger:** `schedule: weekly Tue 06:00`; `repository_dispatch: dependabot.alert.created`.
- **Systems:** reads lockfiles, release notes, Dependabot PRs/alerts; writes one bundled PR.
- **Steps:**
  1. [run] list outdated deps and open Dependabot PRs; baseline tests.
  2. [prompt] `dep-upgrader` bumps one group, reads the changelog, fixes call sites.
  3. [run] build + full tests; on red, retry the repair (budgeted); still red → drop that bump.
  4. [judge] noul "Did any behaviour change beyond the upgrade (tests edited, assertions loosened)?" — yes/uncertain → needs_input.
  5. [approval] → [prompt] `pr-publisher` (closes the superseded bot PRs).
- **Human gate:** PR merge; "Review dependency update PRs for breaking changes" (agentics).
- **Verify:** suite green before and after; the JEV check that tests were not weakened.
- **Success number:** merge rate. dotnet "Update/Upgrade 44 PRs 67.4%".
- **Evidence:** https://devblogs.microsoft.com/dotnet/ten-months-with-cca-in-dotnet-runtime/ · https://github.blog/changelog/2026-04-07-dependabot-alerts-are-now-assignable-to-ai-agents-for-remediation/
- **Skills:** dep-upgrader, change-reviewer, pr-publisher.
- **Ready today?** Yes.

### 5. `duplicate-code-consolidator` — Find and merge copy-pasted logic
- **Job:** Finds duplicated logic above a size threshold and opens a consolidation PR, at most a few per run.
- **Trigger:** `schedule: weekly Wed 06:00`.
- **Systems:** repo; writes issues or a PR.
- **Steps:**
  1. [run] `jscpd` (or equivalent) → blocks >10 lines or in ≥3 places; skip if a `[dup-code]` PR is open.
  2. [prompt] `duplicate-finder` confirms semantic duplicates and proposes one shared function.
  3. [judge] noul "Are these blocks the same behaviour, not just similar text?"
  4. [prompt] `fix-author` consolidates; [run] build + tests.
  5. [approval] → [prompt] `pr-publisher` (max 3 per run).
- **Human gate:** PR merge; refactors change many call sites.
- **Verify:** tests green; duplication count drops (re-run `jscpd`).
- **Success number:** merge rate. "Duplicate Code Detector… 76 merged PRs out of 96 proposed (79%)".
- **Evidence:** https://github.github.com/gh-aw/blog/2026-01-12-welcome-to-pelis-agent-factory/ · https://yegge.ai/essays/six-new-tips-for-better-coding-with-agents/
- **Skills:** duplicate-finder (new), usage-prover, change-reviewer, pr-publisher.
- **Ready today?** Yes (install jscpd in the runner image).

### 6. `test-gap-author` — Tests that would have caught a real bug, proven by mutation
- **Job:** Adds tests for risky untested code and proves they catch defects rather than encode current behaviour.
- **Trigger:** `schedule: weekly Thu 06:00`.
- **Systems:** repo, coverage report; writes a PR.
- **Steps:**
  1. [run] coverage report; rank untested code by churn and call count.
  2. [prompt] `test-author` writes tests for the top N units.
  3. [run] mutation testing on those units (stryker / mutmut / go-mutesting).
  4. [judge] score "mutation kill rate of the new tests"; below threshold → discard the test.
  5. [judge] noul "Does any new test assert behaviour that looks like a bug?" → needs_input.
  6. [approval] → [prompt] `pr-publisher`.
- **Human gate:** PR merge; a test that encodes a bug is "actively harmful" (dotnet).
- **Verify:** mutation score, not coverage.
- **Success number:** merge rate. dotnet "Testing 99 PRs 75.6%". Mutation verification is a named gap ("Improving AI-generated tests using mutation testing" is only a blog proposal).
- **Evidence:** https://devblogs.microsoft.com/dotnet/ten-months-with-cca-in-dotnet-runtime/ · https://blog.senko.net/improving-ai-generated-tests-using-mutation-testing
- **Skills:** test-author, mutation-checker (new), change-reviewer, pr-publisher.
- **Ready today?** Needs a mutation tool per language in the runner image.

**Rejected:** AI review comments on every PR as-is (clause 7: "80% are noise", no cited precision — revisit with a JEV filter) · Ralph while-loop on an existing repo (clause 5: no gate; its author: "no way in heck would I use Ralph in an existing code base") · `/automerge` PR shepherd (clause 5: squashes on green with no human) · "ask the repo" Q&A (clauses 1, 3: chat, no typed output).

---

## debugging-ops

### 1. `ci-failure-doctor` — One deduplicated root-cause issue per CI failure
- **Job:** Reads a failed CI run, finds the first meaningful error, ties it to the commit, and files or updates one issue per root cause.
- **Trigger:** `repository_dispatch: workflow_run.completed[failure]` on main.
- **Systems:** reads GitHub Actions logs, commit, PR; writes/updates a GitHub issue; optional fix PR.
- **Steps:**
  1. [run] fetch failed job logs; skip cancelled runs; list open `[ci]` issues.
  2. [prompt] `ci-log-reader` extracts first error, suspect commit, failure category.
  3. [judge] choice "Same root cause as an open issue? (issue# / new / not actionable)".
  4. [prompt] write or append evidence to the issue.
  5. [judge] noul "Is a fix safe to attempt (not security-sensitive, not a migration)?" — yes → [prompt] `fix-author` on a branch; [run] rerun CI.
  6. [approval] before `pr-publisher` opens the fix PR.
- **Human gate:** the fix PR; GitLab's flow declines when "security-sensitive and should be reviewed by a person".
- **Verify:** CI rerun on the fix branch; the dedup judge keeps one issue per cause.
- **Success number:** merge rate. CI Doctor "9 merged PRs out of 13 proposed (69%)".
- **Evidence:** https://github.com/githubnext/agentics/blob/main/docs/ci-doctor.md · https://docs.gitlab.com/user/duo_agent_platform/flows/foundational_flows/fix_pipeline/
- **Skills:** ci-log-reader (new), repo-navigator, fix-author, pr-publisher.
- **Ready today?** Yes.

### 2. `error-to-fix-pr` — Production error → RCA → plan → PR, with stopping points
- **Job:** Takes a recurring production error, finds the root cause in code, reproduces it and drafts a fix PR.
- **Trigger:** `repository_dispatch: sentry.issue.created` filtered to ≥10 events in 14 days.
- **Systems:** reads Sentry (stack, breadcrumbs), repo; writes Sentry comment, branch, PR.
- **Steps:**
  1. [judge] noul "Is this error actionable in our code (not third-party / noise)?" (validate-bug).
  2. [prompt] RCA with links to code lines and events; `inconclusive` is a valid output.
  3. [prompt] `reproduce-bug` writes a failing test; [run] confirm it fails.
  4. [approval] "Stop after RCA / Plan / PR" setting — the plan is shown before code.
  5. [prompt] `fix-author`; [run] test now passes + full suite; [prompt] `change-reviewer`.
  6. [approval] → [prompt] `pr-publisher`; comment the PR link back on the Sentry issue.
- **Human gate:** plan approval and PR merge. "Nothing merges without a human signature".
- **Verify:** red→green reproduction test; independent diff review.
- **Success number:** dotnet "Bug Fix 317 PRs 69.4%" merged. Seer claims 94.5% root-cause accuracy (vendor, "No methodology rides with the figure").
- **Evidence:** https://docs.sentry.io/product/ai-in-sentry/seer/autofix/ · https://devblogs.microsoft.com/dotnet/ten-months-with-cca-in-dotnet-runtime/
- **Skills:** validate-bug, reproduce-bug, fix-author, change-reviewer, pr-publisher, approval-desk.
- **Ready today?** Yes via the shipped `bug-fixer` template; needs a Sentry webhook forwarder and a Sentry token.

### 3. `flaky-test-fixer` — Reproduce a flake under stress, fix it, prove it with reruns
- **Job:** Takes tests marked flaky, reproduces them, and opens a fix PR only when repeated reruns prove the fix.
- **Trigger:** `schedule: nightly 02:00` over tests that failed then passed on retry.
- **Systems:** CI history, repo; writes a PR or a report.
- **Steps:**
  1. [run] collect flaky candidates from CI history (fail→pass on same commit).
  2. [judge] per test, noul "Is this failure flaky rather than a real regression?" (CCA re-runs only at confidence ≥0.7).
  3. [run] stress-run the test N times to reproduce.
  4. [prompt] `flake-hunter` proposes up to K fixes; [run] each fix × M reruns.
  5. [judge] noul "Do the reruns prove the fix?" — no → report only, no PR (CircleCI: "If the agent lacks confidence… a PR is not created").
  6. [approval] → [prompt] `pr-publisher` (cap on open flaky PRs).
- **Human gate:** PR merge; quarantine hides symptoms, the fix must be reviewed.
- **Verify:** M consecutive green reruns after the fix, fail rate before it.
- **Success number:** no cited success rate. Market gap: "The Peli factory, with 298 workflows, has no flaky-fixer"; CircleCI ships it as beta.
- **Evidence:** https://discuss.circleci.com/t/product-launch-chunk-tasks-fixing-flaky-tests/53975 · https://github.com/anthropics/claude-code-action/blob/main/docs/solutions.md
- **Skills:** reproduce-bug, flake-hunter (new), fix-author, pr-publisher.
- **Ready today?** Yes (needs CI history via `gh` or a test-results store).

### 4. `alert-investigator` — Read-only RCA on a page, with "inconclusive" allowed
- **Job:** When an alert fires, gathers telemetry and recent changes, ranks hypotheses with evidence links, and posts a finding or "inconclusive".
- **Trigger:** `repository_dispatch: pagerduty.incident.triggered` or Alertmanager webhook (p1/p2 only).
- **Systems:** reads metrics/logs (Prometheus, Loki, Datadog), deploy history, git log; writes incident note + Slack thread.
- **Steps:**
  1. [run] pull the alert window, deploys and merged PRs in the last 24h, runbook for the service.
  2. [prompt] `rca-investigator` runs read-only queries, one hypothesis per branch, each with evidence links.
  3. [prompt] adversarial pass: try to refute each hypothesis.
  4. [judge] score per hypothesis "evidence strength"; below threshold → output `inconclusive`.
  5. [run] post to the incident and Slack; if a mitigation is proposed → [approval] before any action.
- **Human gate:** any mitigation (restart, rollback, scale); "Very few are ready to give AI write access to production."
- **Verify:** every claim links to a query result; adversarial refutation pass; replay against past incidents.
- **Success number:** root-cause accuracy on a benchmark. Relvy on OpenRCA: 36% baseline → 48% with a harness; Meta: "42% of these investigations had the root cause in the top five".
- **Evidence:** https://www.relvy.ai/blog/llm-cost-of-ai-sre-investigating-production-alerts · https://engineering.fb.com/2024/06/24/data-infrastructure/leveraging-ai-for-efficient-incident-response/
- **Skills:** rca-investigator (new), repo-navigator, approval-desk.
- **Ready today?** Needs an observability MCP (Prometheus/Loki/Datadog) and a PagerDuty webhook forwarder.

### 5. `perf-regression-fix-forward` — Benchmarked regression → fix PR to the culprit's author
- **Job:** When a benchmark regresses, finds the culprit commit, drafts a fix, and proves the gain with a before/after benchmark.
- **Trigger:** `repository_dispatch: benchmark.regression` (or `schedule: nightly` benchmark run).
- **Systems:** benchmark results, git history; writes a PR requesting review from the culprit author.
- **Steps:**
  1. [run] bisect to the culprit commit with the benchmark.
  2. [prompt] `perf-fixer` drafts a fix from the culprit diff.
  3. [run] benchmark before vs after, several runs.
  4. [judge] noul "Is the improvement outside run-to-run noise?" — no → discard ("no performance PR merges without empirical evidence").
  5. [approval] → [prompt] `pr-publisher` assigning the culprit's author.
- **Human gate:** the culprit author reviews and merges.
- **Verify:** repeated benchmark runs, not the agent's claim of "2x".
- **Success number:** Meta: "~10 hours of manual investigation into ~30 minutes". dotnet Performance PRs merge at 54.5%, the lowest category, without benchmarks.
- **Evidence:** https://engineering.fb.com/2026/04/16/developer-tools/capacity-efficiency-at-meta-how-unified-ai-agents-optimize-performance-at-hyperscale/ · https://devblogs.microsoft.com/dotnet/ten-months-with-cca-in-dotnet-runtime/
- **Skills:** benchmark-runner (new), perf-fixer (new), change-reviewer, pr-publisher.
- **Ready today?** Needs a stable benchmark harness in the repo.

### 6. `ci-speed-coach` — Cut CI time and cost
- **Job:** Reviews the CI config and recent run timings and opens PRs that remove wasted work.
- **Trigger:** `schedule: weekly Fri 06:00`.
- **Systems:** workflow YAML, run durations; writes a PR.
- **Steps:**
  1. [run] pull the last 50 runs' job durations and cache hit rates.
  2. [prompt] propose changes (duplicate test execution, unneeded deps, missing caches), one per PR.
  3. [run] run the changed pipeline on a branch; compare duration and the test count.
  4. [judge] noul "Same tests executed and still green?" — no → drop.
  5. [approval] → [prompt] `pr-publisher`.
- **Human gate:** PR merge (CI config affects everyone).
- **Verify:** identical test count, green, measured minutes saved.
- **Success number:** merge rate. CI Coach "9 merged PRs out of 9 proposed (100% merge rate)".
- **Evidence:** https://github.github.com/gh-aw/blog/2026-01-12-welcome-to-pelis-agent-factory/ · https://github.com/githubnext/agentics
- **Skills:** ci-log-reader (new), change-reviewer, pr-publisher.
- **Ready today?** Yes.

**Rejected:** autonomous production remediation / self-healing (clause 5: "Very few are ready to give AI write access to production") · AI primary on-call that closes alerts as false positives unseen (clause 6: silent closes; "after two or three false positives, engineers stop listening") · auto-published postmortems (clause 4: "really bad at root causes") · `terraform apply` on drift (clause 5: "can silently revert someone's emergency change"; the report-only drift PR passes but has no cited number).

---

## security

### 1. `phishing-report-triage` — User-reported email → verdict → analyst queue
- **Job:** Analyses every email users report, detonates links, and closes clean ones or escalates threats with a rationale.
- **Trigger:** `repository_dispatch: mailbox.reported` (abuse inbox / Outlook report button).
- **Systems:** reads the reported message (Graph/Gmail), VirusTotal, urlscan; writes a ticket and reply to reporter.
- **Steps:**
  1. [run] extract headers, URLs, attachments; reputation lookups.
  2. [prompt] content and header analysis with a written rationale.
  3. [judge] noul "Is this a real phishing threat?" — no → close with rationale, uncertain → analyst, yes → incident.
  4. [approval] before any org-wide purge of the message.
  5. [run] reply to the reporter; store analyst verdicts as calibration data.
- **Human gate:** purge from all mailboxes (irreversible, org-wide).
- **Verify:** detonation results; analyst verdicts feed JEV calibration.
- **Success number:** RCT: "up to 6.5 times as many true positives per analyst minute and a 77% improvement in verdict accuracy… not prone to rubber-stamping". n8n's top SecOps template (#1992) has 26,877 views.
- **Evidence:** https://learn.microsoft.com/en-us/defender-xdr/phishing-triage-agent · https://arxiv.org/abs/2511.13860
- **Skills:** phish-analyst (new), approval-desk.
- **Ready today?** Needs a mailbox connector (Gmail MCP exists; Graph for Outlook) and VirusTotal/urlscan keys.

### 2. `sast-triage-and-fix` — Scanner findings → true/false positive → verified fix PR
- **Job:** Triages static-analysis findings, dismisses false positives with a reason, and fixes true ones with a rescan proving the fix.
- **Trigger:** `schedule: daily 03:00` (backlog) and `repository_dispatch: pull_request.opened` (diff-aware).
- **Systems:** Semgrep/CodeQL SARIF, repo; writes alert dismissals, a PR.
- **Steps:**
  1. [run] scan; take High/Critical first, max 10 per run.
  2. [judge] per finding, noul "Is this a true positive given the data flow?"
  3. [prompt] `fix-author` drafts a fix for true positives.
  4. [run] rescan: the finding is gone and no new one appears; tests green (Snyk's verify-and-retry loop).
  5. [approval] → [prompt] `pr-publisher`; false positives are dismissed only after approval with the rationale.
- **Human gate:** merging the fix and dismissing an alert (both change the security record).
- **Verify:** deterministic rescan of each candidate.
- **Success number:** Semgrep: "Human-agree rate 96%", "Average reduction in findings 60%".
- **Evidence:** https://semgrep.dev/docs/semgrep-assistant/metrics · https://docs.snyk.io/scan-with-snyk/snyk-code/manage-code-vulnerabilities/fix-code-vulnerabilities-automatically
- **Skills:** sast-triager (new), fix-author, change-reviewer, pr-publisher.
- **Ready today?** Yes with Semgrep OSS or CodeQL via `gh`.

### 3. `vuln-dependency-fix` — Dependabot alert → reachability → patched PR
- **Job:** For each security advisory, checks whether the vulnerable code is reachable, upgrades or pins, and repairs the tests.
- **Trigger:** `repository_dispatch: dependabot_alert.created`.
- **Systems:** Dependabot alerts API, repo; writes a PR or a dismissal with VEX-style reason.
- **Steps:**
  1. [run] fetch advisory, affected versions, usage sites.
  2. [judge] noul "Is the vulnerable function reachable from our code?" — no → propose dismissal with reason.
  3. [prompt] `dep-upgrader` upgrades (or downgrades a compromised package when no patch exists).
  4. [run] build + tests; rescan the lockfile.
  5. [approval] merge, or dismissal.
- **Human gate:** merge or dismissal; "Always review the pull request, verify that tests pass".
- **Verify:** lockfile rescan shows the advisory closed; suite green.
- **Success number:** dotnet "Update/Upgrade 44 PRs 67.4%" merged; no cited number for the security subset.
- **Evidence:** https://github.blog/changelog/2026-04-07-dependabot-alerts-are-now-assignable-to-ai-agents-for-remediation/ · https://github.com/githubnext/agentics (vex-generator)
- **Skills:** dep-upgrader, usage-prover, pr-publisher.
- **Ready today?** Yes.

### 4. `security-questionnaire-answerer` — Inbound questionnaire → sourced answers → SME approval
- **Job:** Answers each question from policies and past approved answers, cites the source, and routes weak answers to the right expert.
- **Trigger:** `repository_dispatch: questionnaire.uploaded` (Drive folder or CRM attachment).
- **Systems:** reads policy docs, control list, approved-answer library; writes the filled spreadsheet.
- **Steps:**
  1. [run] parse the questionnaire into rows.
  2. [prompt] draft each answer from the library with a citation.
  3. [judge] per answer, noul "Is this answer supported by the cited source?" — uncertain → SME queue.
  4. [approval] SME approves or edits each flagged answer; owner approves the export.
  5. [run] export and add approved answers back to the library.
- **Human gate:** the export sent to the customer (it is a contractual representation).
- **Verify:** per-answer source check; library only grows from approved answers.
- **Success number:** time per question. Conveyor: Lucid "4 minutes… per questionnaire question" → "22 seconds"; dbt Labs first-pass accuracy 35% → 65% (why review stays).
- **Evidence:** https://www.conveyor.com/ · https://drata.com/products/ai-questionnaire-assistance
- **Skills:** questionnaire-answerer (new), approval-desk.
- **Ready today?** Needs a Drive connector (Google Drive MCP exists) and an approved-answer library.

### 5. `secret-leak-response` — Leaked credential → owner → approved rotation
- **Job:** When a secret leaks, confirms it is real, finds the owner, drafts the rotation plan and verifies revocation after approval.
- **Trigger:** `repository_dispatch: secret_scanning_alert.created` (or push-protection bypass).
- **Systems:** secret-scanning alerts, git blame, provider APIs; writes an issue, rotation run log.
- **Steps:**
  1. [run] alert details, commit, blame, where the secret is used.
  2. [judge] noul "Is this a live credential rather than a test/placeholder value?"
  3. [prompt] rotation runbook for this provider, notify the owner.
  4. [approval] owner approves rotation (breaks running services if done wrong).
  5. [run] rotate/revoke; [run] verify the old credential is rejected.
- **Human gate:** rotation and revocation, the mutating steps.
- **Verify:** old credential tested and refused; alert closed with reason.
- **Success number:** GitGuardian claims "<60s from leak to developer notification" and one case where "17,000 false positives became 1 real exposure" (vendor).
- **Evidence:** https://docs.github.com/en/code-security/secret-scanning/introduction/about-push-protection · https://www.gitguardian.com/
- **Skills:** secret-rotator (new), approval-desk.
- **Ready today?** Needs provider rotation APIs per secret type; detection side works with `gh`.

### 6. `soc2-evidence-review` — Scheduled control evidence check with gap findings
- **Job:** Collects evidence for each control on a schedule, checks it against the criterion, and files gaps before the auditor does.
- **Trigger:** `schedule: monthly 1st 07:00`.
- **Systems:** control list, evidence store (Drive/Git), config APIs; writes findings to the tracker.
- **Steps:**
  1. [run] pull evidence per control (screenshots, config exports, access lists).
  2. [prompt] map evidence to each criterion with citations.
  3. [judge] per control, noul "Does the evidence satisfy the criterion?"
  4. [run] chase attestations only a person can give (notify owners).
  5. [approval] compliance owner accepts findings or ignores with justification.
- **Human gate:** the audit pack sign-off.
- **Verify:** each finding cites a file; JEV judgment calibrated on past auditor findings.
- **Success number:** vendor claims only: Complyance "reducing manual effort by up to 70%"; Vanta "saves customers an average of four hours per week".
- **Evidence:** https://www.complyance.com/ai-agents/soc-2 · https://www.vanta.com/resources/introducing-the-all-new-vanta-ai-agent
- **Skills:** evidence-mapper (new), approval-desk.
- **Ready today?** Needs connectors to the evidence sources.

**Rejected:** autonomous pentest against live third-party sites (clause 5: no gate on outward action) · automatic key revocation with no owner sign-off (clause 5) · AI-generated bug-bounty submissions (clause 4: curl ended its bounty over "AI slop") · threat-intel chat (clause 1).

---
## data

### 1. `data-request-answerer` — Data-request ticket → governed SQL → checked answer
- **Job:** Answers business data requests filed as tickets using only the governed semantic layer, and shows the query behind every number.
- **Trigger:** `repository_dispatch: jira.issue.created[label=data-request]` (or Linear equivalent).
- **Systems:** reads the ticket, semantic layer/dbt models, warehouse (read-only); writes the answer and SQL back to the ticket.
- **Steps:**
  1. [judge] choice "Which domain/metric does this ask about? (list / unclear)" — unclear → ask_human on the ticket.
  2. [prompt] write SQL against endorsed models only; self-check intermediate results (zero rows, bad joins).
  3. [run] execute with a read-only role; run the same question from the golden set if one matches.
  4. [judge] noul "Does the result answer the question as asked?"
  5. [approval] analyst approves before the answer is posted to a non-data requester.
- **Human gate:** posting numbers that people will act on; "pointing Claude at a warehouse… can create a false sense of precision."
- **Verify:** golden-SQL comparisons; the query is attached for inspection.
- **Success number:** Anthropic: "95% of business analytics queries are automated via Claude, with ~95% accuracy in aggregate."
- **Evidence:** https://claude.com/blog/how-anthropic-enables-self-service-data-analytics-with-claude · https://openai.com/index/inside-our-in-house-data-agent/
- **Skills:** sql-analyst (new), approval-desk.
- **Ready today?** Needs a warehouse CLI (bq/psql/snowsql) with read-only creds and a Jira webhook forwarder.

### 2. `pipeline-failure-triage` — dbt/Airflow failure → CODE vs SOURCE → fix PR or source report
- **Job:** On a failed scheduled data job, decides whether our code or the upstream data broke, then opens a fix PR or a root-cause report.
- **Trigger:** `repository_dispatch: dbt.job.failed` (scheduled runs only).
- **Systems:** dbt logs, run artifacts, dev sandbox, KB; writes a PR or a Slack/issue report.
- **Steps:**
  1. [run] download logs, lineage, recent commits; clone prod data slice into a sandbox.
  2. [judge] choice "CODE or SOURCE failure?" ("Classification is the hardest part").
  3. CODE → [prompt] `fix-author` fixes; [run] `dbt build` in the sandbox.
  4. SOURCE → [prompt] root-cause report with diagnostic queries and a stakeholder summary.
  5. [approval] PR merge (CODE) / report send to the source owner (SOURCE).
- **Human gate:** merge of the model change; message to upstream owners.
- **Verify:** sandbox `dbt build` green on the affected models.
- **Success number:** no cited success rate from the deployment; Ordo claims MTTR "from hours to under 60 seconds" (vendor snippet).
- **Evidence:** https://hiflylabs.com/blog/2026/3/5/ai-agent-for-data-pipeline-ops · https://www.useordoai.com
- **Skills:** ci-log-reader (new), fix-author, pr-publisher.
- **Ready today?** Yes for dbt Core in the repo; dbt Cloud needs its webhook.

### 3. `weekly-kpi-narrative` — Metrics pack with a cross-source consistency check
- **Job:** Builds the weekly KPI report, explains material moves, and flags when two sources disagree before anyone reads it.
- **Trigger:** `schedule: Mon 07:00`.
- **Systems:** warehouse/BI, Stripe/CRM exports, sheets; writes a doc/Slack post.
- **Steps:**
  1. [run] pull metrics and deltas vs last week and plan.
  2. [run] reconcile the same metric across sources (e.g. billing vs warehouse revenue).
  3. [judge] per mismatch, noul "Is this discrepancy material?" — yes → flag in the report.
  4. [prompt] narrative for material variances only, each figure linked to its query.
  5. [approval] owner before it goes to execs.
- **Human gate:** distribution to leadership.
- **Verify:** every number traced to a query; source reconciliation step. Boris: Tag "spots a vendor report that disagrees with the numbers, and flags it before moving on" (2026-09-02).
- **Success number:** Poshmark: weekly performance reports "saving hours of manual work every week"; agency reporting "22 hours to 2 hours per month" (integrator claim).
- **Evidence:** https://openai.com/business/guides-and-resources/identifying-and-scaling-ai-use-cases/ · https://swiftheadway.ai/case-studies/ai-automation-marketing-agency-reporting
- **Skills:** sql-analyst (new), report-writer (new).
- **Ready today?** Needs warehouse/BI CLI access.

### 4. `metric-move-rca` — A metric moved; find the segment and the cause
- **Job:** When a tracked metric crosses a threshold, finds the segment behind it and correlates deploys, tickets and campaigns.
- **Trigger:** `repository_dispatch: anomaly.detected` or `schedule: daily 08:00` threshold check.
- **Systems:** analytics, deploy log, ticketing; writes an issue with ranked drivers.
- **Steps:**
  1. [run] detect the move; slice by standard dimensions.
  2. [prompt] correlate with deploys, releases, incidents, marketing changes.
  3. [judge] per driver, score "share of the move explained".
  4. [prompt] report ranked drivers; "inconclusive" allowed.
  5. [run] file the issue to the owning team.
- **Human gate:** none needed for a report; any rollback it suggests goes through that team's own gate.
- **Verify:** drivers must sum to the observed move; each links to its query.
- **Success number:** McKinsey case: "More than 60 percent potential productivity gain" (not yet in production).
- **Evidence:** https://mixpanel.com/blog/root-cause-analysis-data-analytics/ · https://www.mckinsey.com/capabilities/quantumblack/our-insights/seizing-the-agentic-ai-advantage
- **Skills:** sql-analyst (new), rca-investigator (new).
- **Ready today?** Needs analytics API access.

### 5. `record-attribute-extraction` — Fill structured fields from messy text/images, with confidence routing
- **Job:** Extracts attributes (catalogue fields, company facts, categories) into a table and sends only low-confidence rows to an auditor.
- **Trigger:** `repository_dispatch: record.created` (batch of new rows) or `schedule: hourly`.
- **Systems:** source records (product feed, CRM, sheet); writes the target table.
- **Steps:**
  1. [prompt] extract each attribute into a schema.
  2. [judge] per field, noul "Is this extracted value correct for this record?"
  3. [run] yes → write; uncertain → auditor queue; no → leave blank.
  4. [run] weekly sample audit of auto-written rows.
- **Human gate:** auditor review of uncertain rows; never overwrite an existing human-entered value without approval.
- **Verify:** sample audit; per-field calibration.
- **Success number:** Instacart: "95% accuracy in 1 day of work" (was 1 week); a cheaper model saved 70% cost but dropped 60% accuracy on hard attributes.
- **Evidence:** https://www.instacart.com/company/how-its-made/scaling-catalog-attribute-extraction-with-multi-modal-llms · https://n8n.io/workflows/1862
- **Skills:** field-extractor (new).
- **Ready today?** Yes for CSV/sheet sources (`wfx judge` per row); others need their connector.

### 6. `data-quality-alert-rca` — Data-quality alert → lineage walk → owner
- **Job:** When a freshness/volume/schema check fails, walks lineage and recent changes to the likely cause and pages the owner with evidence.
- **Trigger:** `repository_dispatch: dq.alert` (Monte Carlo, dbt tests, Great Expectations).
- **Systems:** lineage, orchestration logs, git; writes an incident note.
- **Steps:**
  1. [run] fetch the failing check, upstream lineage, runs and commits in the window.
  2. [prompt] test hypotheses: data change, system failure, code change.
  3. [judge] choice "Most likely cause (data / system / code / unknown)".
  4. [run] notify the owner of that asset; open an issue.
- **Human gate:** none for diagnosis; any backfill is a separate approved run.
- **Verify:** hypothesis must cite a log line, row count or commit.
- **Success number:** no cited number.
- **Evidence:** https://docs.getmontecarlo.com/docs/troubleshooting-agent · https://datrick.com/ai-data-pipeline-failure-triage-recovery-automation
- **Skills:** rca-investigator (new).
- **Ready today?** Needs the DQ tool's webhook and lineage API.

**Rejected:** open "chat with your database" (clauses 1, 4: no trigger, no governed layer; accuracy "drops from 85% to 20% in production" per a vendor claim) · auto-built dashboards (clause 3: no one acts on them) · text-to-SQL straight to execs (clause 5).

---

## product-design

### 1. `feedback-theme-memo` — Weekly feedback clusters with a named owner
- **Job:** Clusters last week's tickets and interview notes into themes, drops weak ones, and sends each theme to the product owner with linked evidence.
- **Trigger:** `schedule: Mon 08:00`.
- **Systems:** Zendesk/Intercom tickets, interview notes; writes memos to Notion/Linear and notifies owners.
- **Steps:**
  1. [run] pull 7 days of resolved tickets and notes.
  2. [prompt] cluster by theme with customer quotes.
  3. [judge] per cluster, noul "Is this backed by ≥3 distinct tickets?" (Dock drops anything with fewer than three).
  4. [prompt] memo per cluster: pattern, customer language, suspected root cause, 3–5 linked tickets.
  5. [run] route to owner by product surface; [approval] owner marks reviewed or edits.
- **Human gate:** owner review before a theme becomes roadmap input.
- **Verify:** every claim links to ticket IDs; edits kept next to the agent draft.
- **Success number:** OpenAI: "What once took a week of SQL queries and classifiers now happens in a few clicks"; Productboard/Dashlane note processing "from only 50% to… above 80%".
- **Evidence:** https://trydock.ai/blog/cs-feedback-synthesis · https://openai.com/index/openai-research-assistant/
- **Skills:** feedback-clusterer (new).
- **Ready today?** Needs a helpdesk connector.

### 2. `release-notes-from-merges` — Changelog where every line is checked against the diff
- **Job:** Drafts customer-facing release notes from merged PRs and removes any claim the diff doesn't support.
- **Trigger:** `repository_dispatch: release.created` (tag).
- **Systems:** git log, merged PRs; writes CHANGELOG / release draft.
- **Steps:**
  1. [run] list PRs since the last tag.
  2. [judge] per PR, noul "Is this user-visible?"
  3. [prompt] draft grouped notes.
  4. [judge] per line, noul "Does the diff support this statement?" (an LLM "can also invent a change that never shipped").
  5. [approval] PM approves before publishing.
- **Human gate:** publishing to customers.
- **Verify:** line-by-line JEV check against the PR diff.
- **Success number:** merge rate. gh-aw `changeset`: 22/28 (78%).
- **Evidence:** https://github.github.com/gh-aw/blog/2026-01-12-welcome-to-pelis-agent-factory/ · https://evoxiv.com/blog/ai-agents-release-notes-changelog-automation
- **Skills:** release-noter (new), pr-publisher.
- **Ready today?** Yes.

### 3. `issue-intake-triage` — New issue → dedupe → labels → owner, confidence-gated
- **Job:** Labels, deduplicates and routes every new issue, applying only high-confidence changes and suggesting the rest.
- **Trigger:** `repository_dispatch: issues.opened` plus `schedule: daily` sweep of unlabelled issues.
- **Systems:** GitHub/Linear issues; writes labels, comments, assignee.
- **Steps:**
  1. [run] fetch the issue and similar open issues.
  2. [judge] choice "Label from allow-list"; noul "Duplicate of #N?".
  3. [run] yes → apply; uncertain → post as a suggestion; no → leave.
  4. [judge] noul "Is anything needed to reproduce missing?" → ask reporter.
- **Human gate:** closing as duplicate (outward to the reporter) needs approval.
- **Verify:** "All actions are visible in the issue timeline and can be undone"; label accuracy sampled weekly.
- **Success number:** "the 'hello world' of automated agentic workflows"; Copilot applies only high-confidence changes by default. No cited accuracy number.
- **Evidence:** https://docs.github.com/en/copilot/concepts/agents/cloud-agent/about-automation-rationale-and-approvals · https://linear.app/docs/triage-intelligence
- **Skills:** validate-bug, issue-triager (new).
- **Ready today?** Yes for GitHub.

### 4. `a11y-pr-check` — Layered accessibility review of UI changes
- **Job:** Runs deterministic accessibility scanners plus a keyboard-only walk of the changed flow and fixes only mechanical defects.
- **Trigger:** `repository_dispatch: pull_request.opened` touching UI paths.
- **Systems:** repo, preview deploy/Storybook; writes PR comments and a fix commit.
- **Steps:**
  1. [run] lint + axe-core on changed pages.
  2. [prompt] keyboard-only drive of the critical path; capture accessibility-tree snapshots.
  3. [judge] per finding, choice "deterministic defect / needs designer judgment / blocked".
  4. [prompt] fix deterministic defects only; alt text and reading order go to a person.
  5. [approval] before pushing the fix commit to the PR.
- **Human gate:** designer/content owner for judgment items; author approves pushed fixes.
- **Verify:** re-run the scanner; blocked checks marked "blocked rather than passed".
- **Success number:** Deque claims AI guided tests "up to 4x faster" (vendor snippet). No cited merge rate.
- **Evidence:** https://factory.com/articles/accessibility-testing-coding-agents · https://github.com/githubnext/agentics (daily-accessibility-review)
- **Skills:** a11y-reviewer (new), change-reviewer.
- **Ready today?** Needs a browser in the runner (chrome-agent/Playwright-free headless) and a preview URL.

### 5. `design-token-drift` — Figma variables vs code tokens
- **Job:** Diffs the design library against the code's tokens and opens a PR or a design ticket for each drift.
- **Trigger:** `schedule: weekly Mon 06:00`.
- **Systems:** Figma variables API, `tokens.json`; writes a PR or a Figma/issue comment.
- **Steps:**
  1. [run] pull Figma variables; diff with `tokens.json`.
  2. [judge] per drift, choice "code is stale / design is stale / intentional".
  3. [prompt] code-stale → token PR; design-stale → issue to the designer.
  4. [approval] PR merge.
- **Human gate:** merging token changes (visual change everywhere).
- **Verify:** deterministic diff re-run is empty after merge.
- **Success number:** no cited number.
- **Evidence:** https://github.com/maxine-bit/agentic-design-system · https://www.figma.com/solutions/ai-design-system-audit/
- **Skills:** token-differ (new), pr-publisher.
- **Ready today?** Needs a Figma token.

### 6. `spec-from-evidence` — Epic created → PRD draft with linked customer evidence
- **Job:** When a PM opens an epic, drafts the spec with the customer evidence, open questions and an end-to-end verification step.
- **Trigger:** `repository_dispatch: linear.project.created` / Jira epic created.
- **Systems:** tracker, feedback store, repo; writes a spec doc linked to the epic.
- **Steps:**
  1. [run] fetch linked tickets/feedback and related code areas.
  2. [prompt] draft spec: problem, evidence, scope, non-goals, "end-to-end verification step".
  3. [judge] per evidence claim, noul "Is this supported by the linked ticket?"
  4. [prompt] list open questions → ask_human to the PM.
  5. [approval] PM publishes.
- **Human gate:** PM owns the spec.
- **Verify:** evidence claims checked against sources.
- **Success number:** no workflow-specific number; Atlassian's PMs save "nearly 40 minutes per day" across all AI tools.
- **Evidence:** https://www.productboard.com/product/ai/ · https://community.atlassian.com/forums/Atlassian-AI-Rovo-articles/How-the-Atlassian-product-management-team-uses-Rovo-Agents/ba-p/3059291
- **Skills:** spec-writer (new).
- **Ready today?** Needs a tracker connector.

**Rejected:** AI-moderated user interviews (clause 4: "rapport gap", no verification) · auto-generated UI shipped without review (clause 5) · design Q&A bots (clause 1).

---

## support

### 1. `ticket-triage-route` — New ticket → category, priority, team, duplicate
- **Job:** Classifies and routes every new ticket and suggests a reply, sending low-confidence cases to a human.
- **Trigger:** `repository_dispatch: zendesk.ticket.created` (or Freshdesk/HubSpot).
- **Systems:** helpdesk; writes fields, assignee, internal note.
- **Steps:**
  1. [judge] choice "category" and choice "priority P1–P4"; noul "duplicate of an open ticket?"
  2. [run] apply when yes-band; uncertain → triage queue.
  3. [prompt] internal note with suggested next step and KB links.
  4. [run] SLA timer set by priority.
- **Human gate:** none for routing (internal and reversible); replies are separate (next template).
- **Verify:** weekly sample of routed tickets vs agent corrections as JEV calibration.
- **Success number:** ServiceNow: "AI Agents are automating 37% of our customer support case workflow—handling tasks like routing, categorization, and summarization".
- **Evidence:** https://www.servicenow.com/customers/now-on-now-support-ai.html · https://n8n.io/workflows/2468
- **Skills:** ticket-triager (new).
- **Ready today?** Needs a helpdesk connector.

### 2. `grounded-reply-draft` — Reply drafts with every fact checked against the account
- **Job:** Drafts a reply from the customer's account and KB, marks each fact verified or unverified, and leaves sending to an agent.
- **Trigger:** `repository_dispatch: helpdesk.ticket.needs_reply`.
- **Systems:** helpdesk, order/account system, KB; writes a draft reply.
- **Steps:**
  1. [run] fetch account, orders, recent tickets.
  2. [prompt] draft reply grounded in those records and policy.
  3. [judge] per factual claim, noul "Is this claim supported by the account record or KB?" — unsupported claims highlighted.
  4. [approval] support agent sends or edits.
- **Human gate:** every send (Air Canada was held liable for its chatbot's advice).
- **Verify:** per-claim source annotation, like Octopus's verifier.
- **Success number:** Octopus Magic Ink: about a third of drafts need zero or minimal edits, ~35% of emails assisted; Fyxer: "53% Of AI-generated drafts accepted as written".
- **Evidence:** https://www.techuk.org/resource/case-study-kraken-tech-s-generative-ai-tool-for-customer-service.html · https://openai.com/index/fyxer/
- **Skills:** reply-drafter (new).
- **Ready today?** Needs a helpdesk connector; Gmail MCP works for inbox-based support.

### 3. `in-policy-refund` — Refund/return decisions inside a written envelope
- **Job:** Checks each refund request against order, payment and policy, completes clearly in-policy low-value ones, and escalates the rest with context.
- **Trigger:** `repository_dispatch: helpdesk.ticket.created[type=refund]`.
- **Systems:** helpdesk, Shopify/order system, payments; writes refund + reply.
- **Steps:**
  1. [run] fetch order, payment status, prior refunds.
  2. [prompt] policy check with the clause it relies on.
  3. [judge] noul "Is this clearly in policy and low risk?" plus a fixed amount limit.
  4. yes → [run] issue refund; else [approval] human decides with the pre-filled case.
  5. [run] reply to customer; log with policy version.
- **Human gate:** anything outside the envelope, and every decline ("authorizing large refunds" should "trigger human oversight").
- **Verify:** monthly audit sample of auto-refunds.
- **Success number:** WiserBrand: 48% auto-resolved in 60 days, "99% policy-match accuracy across audited low-risk refund decisions", backlog −37% (vendor case).
- **Evidence:** https://wiserbrand.com/case/ai-refund-and-return-agent-for-fashion-retailer/ · https://openai.com/business/guides-and-resources/a-practical-guide-to-building-ai-agents/
- **Skills:** policy-checker (new).
- **Ready today?** Needs Shopify + helpdesk connectors.

### 4. `kb-article-from-ticket` — Resolved ticket → draft KB article
- **Job:** When a ticket closes with a new fix, drafts a KB article and sends it to the KB owner.
- **Trigger:** `repository_dispatch: ticket.solved`.
- **Systems:** helpdesk, KB; writes a draft article.
- **Steps:**
  1. [judge] noul "Does this resolution add something the KB lacks?" (search KB first).
  2. [prompt] draft article with steps from the ticket, customer data removed.
  3. [judge] noul "Is any personal data left in the draft?"
  4. [approval] KB owner publishes.
- **Human gate:** publishing to customers.
- **Verify:** PII check; steps traced to the ticket.
- **Success number:** ServiceNow: "60% of knowledge articles are now AI-generated, accelerating time to publish by an average of 88%".
- **Evidence:** https://www.servicenow.com/customers/now-on-now-support-ai.html · https://github.com/anthropics/knowledge-work-plugins/tree/main/customer-support
- **Skills:** kb-writer (new).
- **Ready today?** Needs helpdesk/KB connector.

### 5. `bug-escalation-pack` — Support ticket → reproducible engineering issue
- **Job:** Turns a "this is a bug" ticket into an engineering issue with impact, repro steps and linked tickets, ready for `error-to-fix-pr`.
- **Trigger:** `repository_dispatch: ticket.tagged[bug]`.
- **Systems:** helpdesk, repo, issue tracker; writes a GitHub/Jira issue and links it back.
- **Steps:**
  1. [judge] noul "Is this a product defect rather than usage/config?" (validate-bug).
  2. [run] find similar tickets for impact count.
  3. [prompt] issue with repro, expected/actual, affected accounts count.
  4. [prompt] `reproduce-bug` tries to reproduce; attach result.
  5. [run] file issue; notify support owner.
- **Human gate:** none to file internally; the fix path has its own gates.
- **Verify:** reproduction attempted and attached.
- **Success number:** no cited number; Boris: Tag "fixes most of our product feedback + bugs" (2026-09-25).
- **Evidence:** https://github.com/anthropics/knowledge-work-plugins/tree/main/customer-support · https://docs.devin.ai/product-guides/auto-triage
- **Skills:** validate-bug, reproduce-bug.
- **Ready today?** Yes with GitHub; helpdesk side needs a connector.

### 6. `sla-escalation-watch` — Tickets about to breach → digest + escalate
- **Job:** Finds tickets at risk of breaching SLA and escalates them before they do.
- **Trigger:** `schedule: every 30 min, business hours`.
- **Systems:** helpdesk; writes Slack digest and reassignments.
- **Steps:**
  1. [run] open tickets with time left, customer tier.
  2. [judge] noul "Will this breach without intervention?"
  3. [run] notify owner; after no acknowledgement in T, notify their lead.
- **Human gate:** reassignment across teams needs the lead's approval.
- **Verify:** breach rate tracked week over week.
- **Success number:** no cited number.
- **Evidence:** https://www.reddit.com/r/automation/comments/1l2voxr/ (Lindy "Watch your escalations" is listed without a URL in the research)
- **Skills:** none beyond the platform.
- **Ready today?** Needs a helpdesk connector.

**Rejected:** fully autonomous tier-1 chatbot (clauses 5, 6: Klarna rehired humans after "lower quality"; "it just... made up an answer") · auto-sent AI replies (clause 5) · WhatsApp bot that answers with no handoff rule (clause 6).

---

## sales

### 1. `inbound-lead-qualify` — Form/email lead → qualify → owner with context
- **Job:** Researches every inbound lead, qualifies it, and hands hot ones to a rep with context while the rest go to nurture.
- **Trigger:** `repository_dispatch: form.submitted` (Typeform/HubSpot).
- **Systems:** form, web, CRM; writes lead fields, owner, Slack alert, draft first email.
- **Steps:**
  1. [prompt] enrich: company, role, "a real person or company".
  2. [judge] choice "hot / warm / junk" with reason.
  3. [run] write CRM fields; hot → assign owner + Slack.
  4. [prompt] draft first reply grounded in product docs.
  5. [approval] rep sends (during rollout "every draft response went back to sales reps").
- **Human gate:** first outbound email.
- **Verify:** rep corrections become JEV calibration data.
- **Success number:** OpenAI inbound: "Accuracy climbed from 60 percent to more than 98 percent" on first emails; Uber "boosts Uber for Business conversions 3X".
- **Evidence:** https://openai.com/index/openai-inbound-sales-assistant/ · https://www.salesforce.com/customer-stories/agibank/agentic-lead-qualification
- **Skills:** lead-researcher (new).
- **Ready today?** Needs CRM connector; Gmail MCP for drafts.

### 2. `pre-call-brief` — Tomorrow's external meetings → cited account briefs
- **Job:** Every morning, writes a brief for each external meeting from CRM history, notes and recent news.
- **Trigger:** `schedule: weekdays 07:00`.
- **Systems:** calendar, CRM, call notes, web; writes a brief to email/Slack.
- **Steps:**
  1. [run] list external meetings in the next 24h.
  2. [prompt] per meeting: account history, open deals, recent news, discovery questions.
  3. [judge] per claim, noul "Is this sourced?" — drop unsourced.
  4. [run] deliver.
- **Human gate:** none (read-only to the rep).
- **Verify:** every line links to a CRM record or URL.
- **Success number:** OpenAI GTM assistant: "a 20% lift in productivity—about one extra day each week"; Rocket Pro prep from "30–60 minutes a day" to under 5 minutes (expected).
- **Evidence:** https://openai.com/index/openai-gtm-assistant/ · https://www.salesforce.com/customer-stories/rocket-pro/agentic-outreach-prep
- **Skills:** account-briefer (new).
- **Ready today?** Needs calendar + CRM connectors.

### 3. `call-notes-to-crm` — Call transcript → CRM update + follow-up, after rep attestation
- **Job:** Turns a recorded call into CRM field updates, next steps and a follow-up email that the rep attests before anything syncs.
- **Trigger:** `repository_dispatch: recording.ready` (Zoom/Gong/Fathom).
- **Systems:** transcript, CRM; writes CRM fields/notes, a draft email.
- **Steps:**
  1. [run] check consent flag; fetch transcript.
  2. [prompt] extract next steps, objections, fields (MEDDIC).
  3. [judge] per field, noul "Does the transcript state this?"
  4. [approval] rep attests ("required attestation before CRM sync").
  5. [run] sync CRM; save follow-up as a draft.
- **Human gate:** CRM overwrite and client email.
- **Verify:** per-field quote from the transcript.
- **Success number:** Morgan Stanley: "follow-ups that used to take days now happen within hours"; COSMO Text2Lead "5 to 7 minutes per lead across more than 2,000 event leads".
- **Evidence:** https://openai.com/index/morgan-stanley/ · https://jump.ai/compliance
- **Skills:** call-extractor (new).
- **Ready today?** Needs recorder + CRM connectors.

### 4. `rfp-intake` — RFP/DDQ upload → requirement matrix → drafted answers → owners
- **Job:** Extracts requirements from an RFP, drafts answers from approved content, and routes each section to an owner.
- **Trigger:** `repository_dispatch: rfp.uploaded`.
- **Systems:** RFP file, approved-answer library, CRM; writes answer sheet, CRM opportunity.
- **Steps:**
  1. [prompt] extract dates, budget, requirements into a matrix.
  2. [prompt] draft each answer from library + latest materials with source references.
  3. [judge] per answer, noul "Is this answer unsupported by sources?" → owner.
  4. [approval] seller approves plan → [run] create opportunity; section owners approve answers.
  5. [run] export; approved answers back into the library.
- **Human gate:** submission to the buyer.
- **Verify:** hallucination-risk check per answer.
- **Success number:** Uber: "~83% faster RFP processing"; Unique: "Cut RFx response time by 80%".
- **Evidence:** https://www.salesforce.com/customer-stories/uber/agentic-rfp-processing · https://www.unique.ai/ai-factory/rfp-support-agent
- **Skills:** rfp-drafter (new), approval-desk.
- **Ready today?** Needs a Drive/CRM connector.

### 5. `crm-hygiene` — Nightly CRM data-quality fixes with owner approval
- **Job:** Finds incomplete, duplicate or stale CRM records and proposes fixes to the record owner.
- **Trigger:** `schedule: nightly 01:00`.
- **Systems:** CRM; writes proposed changes, merges after approval.
- **Steps:**
  1. [run] score each account against core fields; find duplicates.
  2. [judge] noul "Are these two records the same company?"
  3. [prompt] propose field fixes with the source.
  4. [approval] owner approves merges and overwrites.
- **Human gate:** merges (hard to undo).
- **Verify:** re-score after changes.
- **Success number:** COSMO: "reduced data quality support requests by 80 percent"; "96 percent of accounts… meet the company's highest data quality standards".
- **Evidence:** https://learn.microsoft.com/en-us/power-platform/guidance/case-studies/cosmo-consult-improves-sales-operations · https://github.com/anthropics/knowledge-work-plugins/tree/main/sales
- **Skills:** record-matcher (new).
- **Ready today?** Needs a CRM connector.

### 6. `quote-draft-approval` — Deal stage change → priced quote → approval → send
- **Job:** Builds the quote/proposal from the deal and transcript and sends it only after pricing approval.
- **Trigger:** `repository_dispatch: crm.deal.stage_changed[proposal]`.
- **Systems:** CRM, price book, doc template/PandaDoc; writes the PDF, sends email.
- **Steps:**
  1. [prompt] extract scope from notes/transcript.
  2. [run] price from the price book (arithmetic in code, not the model).
  3. [judge] noul "Is the discount inside the rep's authority limit?"
  4. [approval] manager above the limit; rep for all sends.
  5. [run] generate PDF, send, log.
- **Human gate:** send (pricing and commitments).
- **Verify:** totals recomputed deterministically.
- **Success number:** anecdote: "spending an hour to generate an estimate in PandaDoc for every single lead… Now… virtually immediate".
- **Evidence:** https://www.reddit.com/r/automation/comments/1jzp9yz/whats_the_best_automation_youve_built_that/ · https://n8n.io/workflows/4359
- **Skills:** proposal-builder (new).
- **Ready today?** Needs CRM + document connector.

**Rejected:** Google Maps lead scraping (clause 2: no system of record, platform-ban risk; 150,209 views) · autonomous AI SDR sequences (clause 5: sends unreviewed; AI SDR churn "50-70%") · LinkedIn auto-outreach ("will get your LinkedIn account blocked", clause 5).

---
## marketing

### 1. `post-draft-approval` — Scheduled LinkedIn/blog post with an approve/edit/reject gate
- **Job:** Picks the next topic, drafts on-brand posts, and publishes only what a person approves.
- **Trigger:** `schedule: Mon/Wed/Fri 08:00` over a topic sheet.
- **Systems:** topic sheet/Notion, brand guide; writes LinkedIn/CMS post, updates the sheet.
- **Steps:**
  1. [run] next unused topic.
  2. [prompt] draft per channel against the brand guide.
  3. [judge] noul "Does the draft make a factual or product claim not in the source brief?" → flag.
  4. [approval] approve / edit / reject (email or Slack buttons).
  5. [run] publish; mark topic used.
- **Human gate:** publish; the approval is the core of the template.
- **Verify:** claim check against brief; brand-review pass.
- **Success number:** Promega "saved 135 hours in their first six months… for first-draft email campaigns"; n8n's approval template #4005 has 27,869 views.
- **Evidence:** https://n8n.io/workflows/4005 · https://openai.com/business/guides-and-resources/identifying-and-scaling-ai-use-cases/
- **Skills:** brand-writer (new), approval-desk.
- **Ready today?** Needs a LinkedIn/CMS publish connector (drafting part works now).

### 2. `seo-article-to-draft` — Keyword → research → draft → reviewer → CMS draft
- **Job:** Turns one keyword row into a researched, internally-linked article saved as a CMS draft for an editor.
- **Trigger:** `schedule: daily 06:00`, one sheet row per run.
- **Systems:** keyword sheet, SERP/web, WordPress; writes a WP draft and Slack note.
- **Steps:**
  1. [prompt] researcher: JSON outline, focus keyword, sources.
  2. [run] fetch recent posts; pick up to 5 internal links.
  3. [prompt] writer; [prompt] reviewer "for voice, accuracy, and structure".
  4. [judge] per cited fact, noul "Does the source URL support this sentence?"
  5. [run] create WP post in **draft** status; Slack with edit link and run cost.
  6. [approval] editor publishes.
- **Human gate:** publishing (CNET corrected "41 of the 77 stories" written with AI).
- **Verify:** citation check; SEO fields validated.
- **Success number:** Berenberg "generates content 85-90% faster"; one operator reports growth to 200k sessions (anecdote).
- **Evidence:** https://n8n.io/workflows/16554-draft-and-review-seo-wordpress-articles-with-multi-agent-llms-and-slack/ · https://www.theverge.com/2023/1/25/23571082/cnet-ai-written-stories-errors-corrections-red-ventures
- **Skills:** seo-writer (new), citation-checker (new).
- **Ready today?** Needs WordPress credentials.

### 3. `claims-compliance-gate` — Pre-publish brand and regulatory check
- **Job:** Checks every draft asset against approved claims and regulatory rules, with each flag cited, before it goes live.
- **Trigger:** `repository_dispatch: asset.ready_for_review` (CMS/DAM status change).
- **Systems:** draft, approved-claims list, rulebook; writes a flag report, blocks or passes the asset.
- **Steps:**
  1. [prompt] extract every claim.
  2. [judge] per claim, choice "matches approved claim # / not approved / needs disclosure".
  3. [prompt] fix suggestions with citations to the rule.
  4. [approval] legal/brand decides on blocked items.
- **Human gate:** release of anything flagged.
- **Verify:** each flag cites the rule or approved-claim row.
- **Success number:** 360 MMS: "16x faster campaign build time — down from 4 hours to 15 minutes" while enforcing Spam Act and credit disclosures.
- **Evidence:** https://www.salesforce.com/customer-stories/360-mms-marketing · https://writer.com/agents/
- **Skills:** claims-checker (new).
- **Ready today?** Yes for files in a repo/Drive folder.

### 4. `ad-performance-report` — Weekly ads + analytics report with anomaly flags
- **Job:** Pulls ad and analytics data, writes the narrative with every figure sourced, and flags anomalies for the account owner.
- **Trigger:** `schedule: Mon 07:00`.
- **Systems:** Google Ads, Meta Ads, GA4, Search Console; writes report to email/Slack.
- **Steps:**
  1. [run] pull metrics, compute deltas and pacing.
  2. [judge] per metric, noul "Is this move anomalous vs the last 8 weeks?"
  3. [prompt] narrative with source and timestamp on each figure.
  4. [approval] account manager before a client-facing send.
- **Human gate:** client send.
- **Verify:** numbers recomputed from the pulled data.
- **Success number:** Supermetrics: "10-hour reports done in 20 minutes" and "zero hallucinations in that agency's multi-week validation".
- **Evidence:** https://claude.com/customers/supermetrics · https://n8n.io/workflows/2783
- **Skills:** report-writer (new).
- **Ready today?** Needs ads/analytics API connectors.

### 5. `ad-change-proposal` — Daily optimisation proposals created paused
- **Job:** Proposes bid, budget and copy changes, creates them paused, and applies only what a person approves.
- **Trigger:** `schedule: daily 06:00`.
- **Systems:** ad platforms; writes paused campaigns/changes.
- **Steps:**
  1. [run] pull yesterday's performance per campaign.
  2. [prompt] propose changes with the metric behind each.
  3. [judge] noul "Is this change within the account's spend limit?"
  4. [run] create changes in a paused state.
  5. [approval] approval page lists each change; approved ones go live.
- **Human gate:** spend going live ("nothing goes to market without a human actively choosing").
- **Verify:** post-change performance compared at day 7.
- **Success number:** Advolve: "90% reduction in operational work time", "15% increase in customer return on ad spend".
- **Evidence:** https://claude.com/customers/advolve · https://claude.com/customers/supermetrics
- **Skills:** ads-optimizer (new), approval-desk.
- **Ready today?** Needs ad-platform write APIs.

### 6. `competitor-change-digest` — What changed on competitors' sites and reviews
- **Job:** Diffs competitor pages, pricing and reviews against last week and reports only material changes.
- **Trigger:** `schedule: weekly Mon 06:00`.
- **Systems:** web pages, review sites; writes a Slack/Notion digest; state holds last snapshot.
- **Steps:**
  1. [run] fetch pages; diff against stored snapshot.
  2. [judge] per change, noul "Is this change material (pricing, positioning, feature)?"
  3. [prompt] digest with links; battlecard edit proposals.
  4. [approval] CI owner before battlecards change.
- **Human gate:** battlecard updates shared with sales.
- **Verify:** every item links to the before/after snapshot.
- **Success number:** no cited number (Lindy lists it first among "jobs teams hand over first").
- **Evidence:** https://n8n.io/workflows/3101 · https://klue.com/compete-agent
- **Skills:** web-differ (new).
- **Ready today?** Yes (webfetch + state).

**Rejected:** AI short-video factory with auto-publish (clause 5: 214,907 views, brand/likeness risk) · auto-posting to Reddit (clause 5: "unsupervised automated posting is exactly what gets accounts banned") · multi-platform auto-publish without approval (clause 5).

---

## finance

### 1. `expense-policy-review` — Every expense checked against policy, approve-only automation
- **Job:** Reviews each expense against the written policy, auto-approves clearly in-policy ones, and escalates the rest with the rule cited.
- **Trigger:** `repository_dispatch: expense.submitted` (card swipe/receipt).
- **Systems:** card feed, receipts, policy doc; writes approve/flag in the expense tool.
- **Steps:**
  1. [run] match receipt to transaction; duplicate-receipt check.
  2. [prompt] evaluate against the policy with the clause cited.
  3. [judge] noul "Is this clearly in policy?" — yes → approve; else → controller.
  4. [prompt] ask employee for missing info (ask_human).
  5. [approval] controller decides every non-approval (no auto-reject).
- **Human gate:** any rejection or exception.
- **Verify:** sampled audit of auto-approvals.
- **Success number:** Ramp: "99% accuracy", "15x more out-of-policy spend" caught, escalating "only the 10–15%"; GA: "Reclaiming 4-5 hours per week".
- **Evidence:** https://ramp.com/blog/ramp-agents-announcement · https://ramp.com/blog/ramp-policy-agent-ga-launch
- **Skills:** policy-checker (new).
- **Ready today?** Needs expense-tool API.

### 2. `ap-invoice-coding` — Invoice email → extract → GL code → three-way match
- **Job:** Extracts each supplier invoice, codes it, matches it to PO and receipt, and posts only high-confidence ones.
- **Trigger:** `repository_dispatch: mailbox.ap.attachment` (AP inbox).
- **Systems:** AP inbox, ERP (PO, receipts, GL); writes bill drafts or exceptions.
- **Steps:**
  1. [prompt] extract vendor, dates, lines (untrusted document; no write tools in this step).
  2. [run] totals validation and three-way match with tolerances.
  3. [judge] choice "GL code" with confidence; ≥ threshold and matched → draft bill.
  4. [judge] choice "exception class: price / qty / missing receipt".
  5. [approval] by amount threshold before posting.
- **Human gate:** posting/payment approval matrix.
- **Verify:** deterministic match; monthly sample of auto-coded invoices.
- **Success number:** Ledger Summit (composite): ~92% of suggestions accepted, cycle time 8 → 1.4 days; Concentrix: "96 percent accuracy overall, reaching 99 percent".
- **Evidence:** https://ledgersummit.com/case-study-ai-agent-ap-coding-three-way-match.html · https://learn.microsoft.com/en-us/power-platform/guidance/case-studies/concentrix-invoice-processing
- **Skills:** doc-extractor (new), approval-desk.
- **Ready today?** Needs ERP API (QuickBooks/Xero/NetSuite); Gmail MCP covers intake.

### 3. `bank-rec-exceptions` — Bank feed → exact match → rules → judged fuzzy match
- **Job:** Reconciles bank lines to the ledger, auto-matching only high-confidence ones and listing the rest for review.
- **Trigger:** `schedule: daily 06:00` after the bank feed lands.
- **Systems:** bank feed, ledger; writes matches and an exceptions list.
- **Steps:**
  1. [run] exact matches; bank rules.
  2. [judge] per remaining line, noul "Is this the matching invoice/transaction?"
  3. [run] yes → match; uncertain → exceptions list.
  4. [approval] bookkeeper clears exceptions.
- **Human gate:** clearing exceptions.
- **Verify:** balances tie after matching; weekly review cadence.
- **Success number:** Intuit reconciles "nearly 3 times faster"; bookkeeping categorisation runs "85-95%" with a "2-5% confidently wrong residual".
- **Evidence:** https://central.xero.com/0/article/About-auto-bank-reconciliation-powered-by-JAX · https://beancount.io/blog/2026/05/13/ai-powered-bookkeeping-automation-generative-ai-transaction-categorization-reconciliation-real-time-reporting-small-business-guide
- **Skills:** record-matcher (new).
- **Ready today?** Needs ledger/bank API; works now on CSV exports.

### 4. `month-end-close-pack` — Accrual drafts, roll-forwards and flux, no posting
- **Job:** Prepares the close package — draft journal entries, roll-forwards and variance commentary — for the controller.
- **Trigger:** `schedule: business day 1, 06:00`.
- **Systems:** GL trial balance, subledgers, budget; writes the close package and draft JEs.
- **Steps:**
  1. [run] pull trial balance and prior period.
  2. [prompt] accrual schedule with JE drafts (parallel with roll-forwards).
  3. [run] roll-forward ties: "beginning + activity − reversals = ending".
  4. [judge] per line over threshold, noul "Is this variance explained by a transaction driver?"
  5. [prompt] critic re-checks each figure against sources.
  6. [approval] controller; posting happens outside the agent.
- **Human gate:** JE posting ("No GL posting… posting requires controller approval").
- **Verify:** deterministic ties, independent critic.
- **Success number:** Numeric: Brex cut close "from 6 to 4 days"; Public.com took 2 days off.
- **Evidence:** https://www.numeric.io/solutions/variance-analysis-software · https://github.com/anthropics/financial-services/blob/main/plugins/agent-plugins/month-end-closer/agents/month-end-closer.md
- **Skills:** close-preparer (new), approval-desk.
- **Ready today?** Needs GL export/API.

### 5. `ar-dunning` — Overdue invoices → staged reminders → human at final stage
- **Job:** Sends tone-appropriate reminders on overdue invoices and holds any final-notice or collections language for approval.
- **Trigger:** `schedule: daily 09:00`.
- **Systems:** accounting AR, email; writes emails, notes on invoice.
- **Steps:**
  1. [run] overdue invoices by age; exclude disputed.
  2. [judge] noul "Is this customer disputing the invoice?" (from recent emails) → route to AR.
  3. [prompt] draft reminder for the stage (3/7/14 days).
  4. [run] send early stages; [approval] final-notice/collections stage.
- **Human gate:** final notice and anything mentioning collections.
- **Verify:** payment status re-checked right before send.
- **Success number:** QuickBooks: "Get paid 4 days faster on average when you send invoice reminders"; HighRadius: "Reduce past dues by 20%".
- **Evidence:** https://quickbooks.intuit.com/payments-agent/ · https://www.highradius.com/software/order-to-cash/collections-management/
- **Skills:** reminder-writer (new).
- **Ready today?** Needs accounting API; Gmail MCP for sending drafts.

### 6. `underwriting-submission-triage` — Broker submission → extract → appetite → ranked queue
- **Job:** Extracts each insurance submission, checks it against appetite, lists missing information, and ranks the underwriter's queue.
- **Trigger:** `repository_dispatch: mailbox.submissions` (broker inbox).
- **Systems:** submissions inbox, appetite rules, policy admin; writes queue entries and broker requests.
- **Steps:**
  1. [prompt] extract insured, limits, loss history (sandboxed reader).
  2. [run] appetite rules; missing-info check ("missing loss runs").
  3. [judge] score "propensity to bind".
  4. [prompt] draft request for missing info; [approval] underwriter sends it.
  5. [approval] underwriter decides; the system "does not make binding decisions autonomously".
- **Human gate:** quote/bind/decline decisions.
- **Verify:** extracted fields cite the document span.
- **Success number:** AIG: review ">5x" faster, data accuracy "from 75% to over 90%", Lexington "+35% submit-to-bind".
- **Evidence:** https://actuary.info/insights/aig-agentic-ai-underwriting-machine · https://www.anthropic.com/news/claude-for-financial-services
- **Skills:** doc-extractor (new).
- **Ready today?** Needs policy-admin connector; intake works with Gmail MCP.

**Rejected:** agent-executed payments (clause 5: every reference design stages for sign-off) · payroll release without review (clause 5) · receipt-to-sheet bots with no review cadence (clause 4: "2-5% confidently wrong residual is where audit risk hides").

---

## legal

### 1. `brief-citation-check` — Every citation resolved against a real database before filing
- **Job:** Extracts every case citation from a draft brief, confirms each exists and supports the sentence, and blocks filing until the signer clears every flag.
- **Trigger:** `repository_dispatch: document.ready_for_filing` (DMS status) or `workflow_dispatch` with the file.
- **Systems:** the draft (Word/PDF), CourtListener or Lexis/Westlaw; writes a flag report on the document.
- **Steps:**
  1. [run] extract citations deterministically.
  2. [run] resolve each against the authoritative database (CourtListener has ~9M opinions).
  3. [judge] per resolved citation, noul "Does the cited opinion support the proposition in this sentence?"
  4. [prompt] report: not found / mismatched / supported, with quoted passages.
  5. [approval] signing attorney clears every flag.
- **Human gate:** filing; "the person who signs cannot delegate verification".
- **Verify:** existence is a database lookup, never the model's own word (Mata v. Avianca).
- **Success number:** 2,079 court decisions on AI-fabricated citations in the Charlotin database; ≥$145k US sanctions in Q1 2026.
- **Evidence:** https://www.damiencharlotin.com/hallucinations/ · https://benchrecon.com/tools/citation-check
- **Skills:** citation-checker (new).
- **Ready today?** Yes with the free CourtListener API (US case law); other jurisdictions need their database.

### 2. `nda-triage` — Inbound NDA → envelope check → auto-approve or lawyer
- **Job:** Screens each inbound NDA against the pre-approved position, approves those inside it, and sends the rest to a lawyer with deviations highlighted.
- **Trigger:** `repository_dispatch: mailbox.legal.nda` or CRM attachment.
- **Systems:** NDA file, playbook; writes GREEN/YELLOW/RED verdict, log with policy version.
- **Steps:**
  1. [prompt] extract term, governing law, mutuality, carve-outs.
  2. [judge] choice "GREEN / YELLOW / RED" against the playbook, per clause.
  3. GREEN → [run] route for signature, log with policy version.
  4. YELLOW/RED → [approval] lawyer with deviations highlighted.
  5. Shadow mode first: three months of past NDAs, lawyer confirms each would-be auto-approval.
- **Human gate:** anything outside the envelope; "Anything outside it escalates, no exceptions".
- **Verify:** shadow-mode agreement rate before auto-approval is enabled.
- **Success number:** claimed "60–80% reduction in NDA turnaround time".
- **Evidence:** https://transformationplaybook.ai/use-cases/legal-02 · https://github.com/anthropics/knowledge-work-plugins/tree/main/legal
- **Skills:** playbook-reviewer (new), approval-desk.
- **Ready today?** Yes for documents in a folder; e-sign routing needs a connector.

### 3. `contract-playbook-review` — Clause-by-clause redlines against the company playbook
- **Job:** Compares an incoming contract to the playbook, rates each clause, and proposes redline language for a lawyer to accept or reject.
- **Trigger:** `repository_dispatch: contract.received` (email/Salesforce/DMS).
- **Systems:** contract, playbook; writes a redline draft and risk memo.
- **Steps:**
  1. [prompt] clause extraction (liability, indemnity, IP, DPA, term, law).
  2. [judge] per clause, choice "GREEN / YELLOW / RED"; escalation triggers like "Uncapped liability".
  3. [prompt] redline language + business impact note.
  4. [run] re-run the review for consistency (Spellbook runs it "ten times in a row").
  5. [approval] lawyer accepts/rejects each suggestion; nothing is sent to the counterparty automatically.
- **Human gate:** sending markup to the counterparty.
- **Verify:** consistency re-runs; acceptance rate per suggestion tracked.
- **Success number:** Spellbook: "10x faster… from 10 hours of lawyer time to one"; "530,000 contract reviews per month".
- **Evidence:** https://claude.com/customers/spellbook · https://ironcladapp.com/resources/articles/ai-agentic-launch
- **Skills:** playbook-reviewer (new).
- **Ready today?** Yes (.docx via the docx skill); DMS intake needs a connector.

### 4. `obligation-renewal-monitor` — Contract dates and escalations never missed
- **Job:** Extracts renewal windows, notice deadlines and escalation clauses once, then reminds owners before each deadline.
- **Trigger:** `repository_dispatch: contract.executed` (extract) + `schedule: daily 07:00` (reminders).
- **Systems:** executed contracts; writes a deadline table (state) and notifications.
- **Steps:**
  1. [prompt] extract dates and obligations with the clause span.
  2. [judge] per field, noul "Is this date correctly extracted from the cited clause?" — uncertain → abstractor.
  3. [run] daily: deadlines at T-90/T-30/T-7 → notify owner.
  4. [approval] owner decides renew / renegotiate / terminate.
- **Human gate:** the renewal decision.
- **Verify:** field-level confidence and span citation.
- **Success number:** JLL lease abstraction: "cut manual review labor by 60% and surfaced over $1 million in missed escalation clauses".
- **Evidence:** https://www.outcomecatalyst.com/blog/ai-agents-commercial-real-estate-workflow-automation · https://iron.eight25sites.com/resources/articles/the-reality-of-ai-agents-in-legal-operations-today
- **Skills:** field-extractor (new).
- **Ready today?** Yes for a folder of contracts.

### 5. `regulatory-change-brief` — Scheduled horizon scan → cited action memo
- **Job:** Scans regulators and trusted sources for changes relevant to the business and writes a cited memo of what to do.
- **Trigger:** `schedule: weekly Mon 07:00`.
- **Systems:** regulator feeds, internal policies; writes memo and tasks.
- **Steps:**
  1. [run] pull new items per jurisdiction since last run (state).
  2. [judge] per item, noul "Is this relevant to our obligations?"
  3. [prompt] cited memo: change, affected policy, action.
  4. [run] citation existence check on every link.
  5. [approval] counsel before it goes to leadership.
- **Human gate:** distribution to leadership and any policy change.
- **Verify:** every claim linked; Deloitte refunded a report with "references to non-existent academic research papers".
- **Success number:** no cited outcome for scanning itself; a related cited-lookup tool "reduced research time by 90 percent, saving more than 10,000 hours each year" (Dunaway).
- **Evidence:** https://www.harvey.ai/platform/agents · https://learn.microsoft.com/en-us/power-platform/guidance/case-studies/dunaway-streamlines-city-code-research
- **Skills:** reg-scanner (new), citation-checker (new).
- **Ready today?** Yes for public sources via webfetch.

### 6. `privilege-first-pass` — Recall-tuned privilege calls with draft log entries
- **Job:** Marks likely-privileged documents with a rationale and a draft privilege-log line, and leaves every final call to an attorney.
- **Trigger:** `repository_dispatch: review_batch.ready` after collection.
- **Systems:** document set, entity list; writes privilege predictions and draft log.
- **Steps:**
  1. [approval] setup: attorneys label each firm/person as privilege-conferring, breaking or neutral.
  2. [judge] per document, noul "Is this likely privileged?" with the threshold tuned for recall.
  3. [prompt] rationale, citations and draft log description.
  4. [approval] attorney makes the final call.
- **Human gate:** production to the other side (waiver risk).
- **Verify:** sample QC of predicted-not-privileged documents.
- **Success number:** Arnold & Porter: about 75% cost saving vs linear contract-attorney review in one matter (with the caveat it may not always be cheaper).
- **Evidence:** https://help.relativity.com/RelativityOne/Content/Relativity/aiR_for_Privilege/aiR_for_Privilege.htm · https://www.arnoldporter.com/en/perspectives/blogs/edata-edge/2026/04/generative-ai-for-privilege-review
- **Skills:** privilege-reviewer (new).
- **Ready today?** Yes on exported document folders (`wfx judge` per doc); e-discovery platforms need connectors.

**Rejected:** agent-to-agent autonomous negotiation (clause 7: Luminance's was "a simulated negotiation", no production outcome) · consumer "robot lawyer" advice (clause 4: FTC found DoNotPay "did not test whether its 'AI lawyer' operated to the level of a human lawyer") · auto-filed disclosures such as 8-K (clause 5).

---

## hr

### 1. `cv-screen-rubric` — Applications scored against the JD rubric, never auto-rejected
- **Job:** Parses each application, scores it against the rubric with evidence, and ranks the list for a recruiter who decides.
- **Trigger:** `repository_dispatch: ats.application.created` (or application email).
- **Systems:** ATS/email, CV PDFs, rubric; writes scores and notes to ATS.
- **Steps:**
  1. [prompt] extract experience/skills with the CV line as evidence.
  2. [judge] per rubric criterion, score with evidence; personal attributes excluded.
  3. [run] ranked list to the recruiter.
  4. [approval] recruiter decides advance/reject; the email is sent only after that.
- **Human gate:** any advance or rejection message (bias and legal risk).
- **Verify:** criterion scores cite CV lines; periodic bias check across groups.
- **Success number:** vendor-reported: Accenture "reduced time-to-fill by 25% across 7M applications annually" (Workday); n8n's top HR template (#2860) has 50,878 views.
- **Evidence:** https://www.workday.com/en-us/artificial-intelligence/ai-agents/talent-acquisition.html · https://n8n.io/workflows/2860
- **Skills:** rubric-scorer (new).
- **Ready today?** Yes via email + Drive MCPs; ATS needs a connector.

### 2. `interview-scheduler` — Qualified candidate → slots → booked → reschedules handled
- **Job:** Once a recruiter qualifies a candidate, offers slots, books the interview and handles reschedules.
- **Trigger:** `repository_dispatch: ats.candidate.stage_changed[qualified]` (a human decision is the trigger).
- **Systems:** ATS, interviewer calendars, SMS/email; writes calendar holds and ATS updates.
- **Steps:**
  1. [run] free/busy for the panel.
  2. [prompt] message the candidate with slots.
  3. [judge] choice "candidate picked slot / asks to reschedule / other" → other goes to coordinator.
  4. [run] book and notify recruiter.
- **Human gate:** the qualification that triggers it; coordinator handles "other".
- **Verify:** calendar re-checked for conflicts before booking.
- **Success number:** Workday: "12,000 hours saved annually", "92% decrease in time-to-schedule".
- **Evidence:** https://www.workday.com/en-us/customer-stories/q-z/workday-saves-thousands-hours-annually-conversational-ai.html · https://n8n.io/workflows/3363
- **Skills:** scheduler (new).
- **Ready today?** Needs calendar connector (Google/Outlook) and ATS webhook.

### 3. `hr-helpdesk-answer` — HR ticket → cited policy answer or specialist
- **Job:** Answers HR helpdesk tickets from the handbook with the source cited, and routes sensitive or unclear ones to a specialist.
- **Trigger:** `repository_dispatch: hr_ticket.created` (ServiceNow/Jira SM/email).
- **Systems:** handbook, HRIS attributes (location, contract); writes the ticket reply.
- **Steps:**
  1. [judge] noul "Is this sensitive (legal, personnel decision, grievance)?" — yes → specialist, no draft.
  2. [prompt] answer from the handbook, personalised by location/contract, with citation.
  3. [judge] noul "Is the answer supported by the cited section?"
  4. yes → [run] reply; else → HR specialist.
- **Human gate:** sensitive topics go straight to a person.
- **Verify:** citation support check.
- **Success number:** IBM AskHR: "94% containment rate of common questions", "75% reduction in support tickets".
- **Evidence:** https://www.ibm.com/case-studies/ibm-askhr · https://learn.microsoft.com/en-us/microsoft-365/copilot/employee-self-service/overview
- **Skills:** policy-answerer (new).
- **Ready today?** Needs a ticketing connector; handbook in Drive works via MCP.

### 4. `onboarding-provision` — New hire → accounts, groups, plan; above-baseline access approved
- **Job:** On a new hire, creates baseline accounts, the first-week plan and buddy intro, and asks approval for anything beyond baseline access.
- **Trigger:** `repository_dispatch: hris.hire.created`.
- **Systems:** HRIS, Google Workspace/M365, Slack, GitHub; writes accounts and a checklist.
- **Steps:**
  1. [run] baseline accounts by role template.
  2. [judge] noul "Does requested access exceed the role baseline?" → approval.
  3. [prompt] first-week plan and welcome message.
  4. [approval] manager confirms plan and extra access.
  5. [run] verify each account exists and has only the approved groups.
- **Human gate:** access grants above baseline.
- **Verify:** post-provision access diff against approved list.
- **Success number:** no cited number.
- **Evidence:** https://learn.microsoft.com/en-us/power-platform/guidance/case-studies/aecom-streamlined-onboarding · https://n8n.io/workflows/3860
- **Skills:** access-provisioner (new).
- **Ready today?** Needs admin APIs for each system.

### 5. `offboarding-revoke` — Leaver → suspend, transfer, revoke, verify
- **Job:** On a termination date, suspends accounts, transfers ownership, opens the asset-recovery ticket and proves access is gone.
- **Trigger:** `schedule: daily 00:05` over HRIS termination dates.
- **Systems:** HRIS, Workspace/M365, Slack, GitHub, CRM; writes suspensions, transfers, ticket.
- **Steps:**
  1. [run] inventory the leaver's accounts and owned assets.
  2. [run] suspend sign-in (reversible) immediately.
  3. [approval] data transfer and deletion (irreversible).
  4. [run] transfer, then delete per retention policy.
  5. [run] verify no active sessions/tokens remain.
- **Human gate:** deletion and data transfer.
- **Verify:** access check after revocation.
- **Success number:** no cited number (templates exist but under 10 views — a thin-supply gap).
- **Evidence:** https://n8n.io/workflows/16403 · https://n8n.io/workflows/15690
- **Skills:** access-provisioner (new).
- **Ready today?** Needs admin APIs.

### 6. `payroll-anomaly-check` — Pay run diffed and anomalies held before release
- **Job:** Compares each pay run with the last, explains expected changes, and holds anomalies for payroll to clear before release.
- **Trigger:** `repository_dispatch: payroll.run.prepared`.
- **Systems:** payroll export, HRIS changes; writes an anomaly report.
- **Steps:**
  1. [run] diff per employee vs last run.
  2. [run] match diffs to HRIS events (raise, leave, joiner).
  3. [judge] per unexplained diff, noul "Is this an error rather than an expected change?"
  4. [approval] payroll lead releases the run.
- **Human gate:** release of payroll.
- **Verify:** every diff either matched to an event or cleared by a person.
- **Success number:** BCG: a payroll provider "improving processing speed by more than 50%".
- **Evidence:** https://www.bcg.com/publications/2025/agents-accelerate-next-wave-of-ai-value-creation · https://n8n.io/workflows/16197
- **Skills:** record-matcher (new).
- **Ready today?** Yes on exported payroll files.

**Rejected:** auto-sent rejection emails from AI scores (clause 5) · AI-written performance reviews without the manager (clause 5: Workday keeps "a human in the loop on every output") · engagement surveillance on employee messages (clause 3: no legitimate action taker).

---
## operations

### 1. `it-l1-ticket-resolver` — Routine IT tickets resolved inside permissions
- **Job:** Resolves password resets, unlocks, software access and VPN tickets within defined permissions, and escalates what it doesn't know.
- **Trigger:** `repository_dispatch: it_ticket.created` (ServiceNow/Jira SM/Slack form).
- **Systems:** ticketing, IdP, software catalogue; writes resolution or escalation.
- **Steps:**
  1. [judge] choice "category (reset / unlock / access / VPN / other)".
  2. [judge] noul "Can this be resolved with allowed actions?" — no/uncertain → human queue.
  3. [approval] software-access requests go through the existing approval chain.
  4. [run] perform the allowed action; [run] verify it worked (user can sign in / has the group).
  5. [run] close with steps taken.
- **Human gate:** access grants and anything outside the allowed action list.
- **Verify:** post-action check; "the autonomous worker will know what it doesn't know".
- **Success number:** ServiceNow: "over 90% of targeted Level 1 volume is handled autonomously, with resolution rates above 99%"; Mercari: "74% of IT tickets are automatically resolved".
- **Evidence:** https://www.theregister.com/software/2026/02/26/servicenow-ai-bot-is-resolving-90-of-our-help-desk-tickets/4825725 · https://www.moveworks.com/us/en/customers/mercari-reduced-it-ticket-volume-moveworks-conversational-ai
- **Skills:** it-resolver (new), approval-desk.
- **Ready today?** Needs ticketing + IdP admin APIs.

### 2. `po-past-due-chaser` — Past-due PO lines → supplier emails → ERP updated
- **Job:** Emails suppliers about past-due PO lines, reads their replies, and updates promised dates in the ERP.
- **Trigger:** `schedule: daily 08:00`.
- **Systems:** ERP open PO lines, supplier email; writes emails and ERP dates.
- **Steps:**
  1. [run] list past-due lines; group by supplier.
  2. [prompt] draft one email per supplier.
  3. [run] send (after an initial shadow period of approved drafts).
  4. [prompt] parse replies; [judge] noul "Does the reply commit to a specific new date?"
  5. yes → [run] update ERP; else → buyer queue.
- **Human gate:** buyer approves drafts during shadow mode; ambiguous replies always.
- **Verify:** ERP update only from a quoted reply line.
- **Success number:** Didero: past-due items "2,300… to 261, in a matter of a few weeks".
- **Evidence:** https://blog.didero.ai/blog/customer-voice · https://www.didero.ai
- **Skills:** supplier-mailer (new).
- **Ready today?** Needs ERP API; Gmail MCP covers email.

### 3. `supplier-inbox-to-erp` — Supplier emails resolved, shadow mode first
- **Job:** Reads supplier emails about orders, recommends the ERP change with an audit trail, and graduates to acting alone per category.
- **Trigger:** `repository_dispatch: mailbox.purchasing` (new supplier email).
- **Systems:** purchasing inbox, ERP; writes ERP changes or recommendations.
- **Steps:**
  1. [judge] choice "intent (confirmation / delay / price change / invoice / other)".
  2. [prompt] map to the PO line and proposed ERP change.
  3. [judge] noul "Is this change inside the category's autonomy envelope?"
  4. inside → [run] apply; outside → [approval] buyer.
- **Human gate:** price changes and anything outside the envelope.
- **Verify:** ERP diff logged next to the email.
- **Success number:** Didero at FABCO: "50% team capacity… in 6 months".
- **Evidence:** https://www.didero.ai · https://blog.didero.ai/blog/customer-voice
- **Skills:** supplier-mailer (new).
- **Ready today?** Needs ERP API.

### 4. `purchase-request-to-po` — Intake → policy route → supplier vetting → approval → PO
- **Job:** Takes a purchase request, checks approved vendors and budget, vets new suppliers, and routes approvals by threshold before creating the PO.
- **Trigger:** `repository_dispatch: intake_form.submitted`.
- **Systems:** intake form, vendor list, budget, ERP; writes approvals and PO.
- **Steps:**
  1. [prompt] extract item, amount, vendor.
  2. [judge] noul "Is there an approved vendor/tool that already covers this?" → redirect.
  3. [prompt] supplier risk check for new vendors.
  4. [approval] approver chain by amount.
  5. [run] create PO.
- **Human gate:** spend approval.
- **Verify:** budget and vendor checks are deterministic.
- **Success number:** vendor claim: "cut cycle times by 40–60%" (Zip, no named customer).
- **Evidence:** https://zip.com/blog/ai-in-procurement · https://github.com/anthropics/knowledge-work-plugins/tree/main/operations
- **Skills:** policy-checker (new), approval-desk.
- **Ready today?** Needs ERP API.

### 5. `missed-pickup-resolver` — Freight exceptions chased and decided
- **Job:** For each missed pickup, contacts the carrier, decides the next step and rebooks within rules, escalating the rest.
- **Trigger:** `schedule: every 30 min` over TMS exceptions.
- **Systems:** TMS, carrier email/phone; writes rebookings and customer notes.
- **Steps:**
  1. [run] missed pickups from the TMS.
  2. [prompt] contact carrier, gather facts.
  3. [judge] choice "next step (rebook same carrier / new carrier / notify customer / escalate)".
  4. [approval] rebooks above a cost limit.
  5. [run] update TMS; notify customer.
- **Human gate:** above-limit spend and customer-impacting changes.
- **Verify:** TMS reflects the new appointment.
- **Success number:** C.H. Robinson: "95% of checks on missed LTL pickups have been automated, saving over 350 hours of manual work per day"; return trips −42%.
- **Evidence:** https://www.chrobinson.com/en-us/about-us/newsroom/press-releases/2026/chrobinson-lean-ai-agents-for-ltl-pickup-efficiency/ · https://www.project44.com/press-releases/project44-launches-ai-ocean-exceptions-agent-to-autonomously-resolve-rolled-container-disruptions/
- **Skills:** carrier-contact (new).
- **Ready today?** Needs TMS API; voice needs a voice provider.

### 6. `maintenance-request-triage` — Resident/facility request → emergency or work order
- **Job:** Classifies each maintenance request, pages on-call for emergencies, and creates a work order for the rest.
- **Trigger:** `repository_dispatch: maintenance.request.created`.
- **Systems:** request portal/email, CMMS; writes work orders, pages.
- **Steps:**
  1. [judge] noul "Is this an emergency (water, gas, no heat, safety)?" — yes/uncertain → page on-call.
  2. [prompt] troubleshooting reply for simple issues.
  3. [run] create work order with category and priority.
  4. [run] no acknowledgement in T → next tier (escalation ladder).
- **Human gate:** on-call confirms emergency response.
- **Verify:** uncertain always pages a person (recall over precision).
- **Success number:** EliseAI at Summit: "de-escalated 34% of maintenance emergencies".
- **Evidence:** https://www.casestudies.com/company/eliseai/case-study/how-summit-property-management-reinvented-their-management-model-with-ai-and-centralization · https://eliseai.com/platform-overview
- **Skills:** none beyond the platform.
- **Ready today?** Needs CMMS connector and a paging webhook.

**Rejected:** plan-level approval that fans out into untracked actions (clause 6: no per-action audit) · auto-rebooking freight without an analyst (clause 5: project44 keeps "full authority over rebooking decisions" with analysts) · chat assistant for warehouse staff (clause 1).

---

## healthcare

*Rule for every template here: the JEV band may approve or route; any denial or clinical decision goes to a named licensed person, with their review time recorded.*

### 1. `portal-message-draft` — Patient portal message → classified → draft reply for a clinician
- **Job:** Classifies each portal message and drafts a reply from the chart for messages that don't need clinical decisions; a clinician sends every one.
- **Trigger:** `repository_dispatch: ehr.inbasket.message`.
- **Systems:** EHR In Basket, chart (meds, results); writes a draft reply.
- **Steps:**
  1. [judge] choice "general / results / medications / paperwork / clinical decision".
  2. clinical decision → route to clinician, no draft.
  3. [prompt] draft from chart data with sources.
  4. [approval] clinician starts from the draft or discards; sends.
- **Human gate:** every send; "Nothing auto-sends."
- **Verify:** draft cites chart items; draft usage rate tracked.
- **Success number:** Mayo: "3.9 million patient messages generated a draft", ~30 s saved per message, ~1,500 hours/month; Stanford RCT: 20% draft use and "no change in time" (burnout fell).
- **Evidence:** https://www.epicshare.org/share-and-learn/mayo-ai-message-responses · https://pmc.ncbi.nlm.nih.gov/articles/PMC10955355/
- **Skills:** chart-reader (new).
- **Ready today?** Needs an EHR connector (FHIR/Epic).

### 2. `prior-auth-packet` — Medication PA → payer questions answered from the chart → staff submit
- **Job:** Answers the payer's prior-authorization questions from chart evidence and hands the packet to staff to review and submit.
- **Trigger:** `repository_dispatch: ehr.workqueue.pa_required`.
- **Systems:** EHR, payer question set; writes the PA form draft.
- **Steps:**
  1. [run] fetch payer questions and relevant chart data.
  2. [prompt] answer each question citing the chart item.
  3. [judge] per answer, noul "Does the cited chart evidence support this answer?"
  4. [approval] staff accept/edit, then submit.
- **Human gate:** submission to the payer.
- **Verify:** per-answer evidence check.
- **Success number:** Summit Health on Epic: PA submission time −42%, "92% of AI-generated responses accepted without edits".
- **Evidence:** https://www.beckershospitalreview.com/healthcare-information-technology/ehrs/epic-ai-adoption-surpasses-85-of-customers/ · https://www.anthropic.com/news/healthcare-life-sciences
- **Skills:** chart-reader (new).
- **Ready today?** Needs EHR + payer portal connectors.

### 3. `denial-appeal-package` — Denial remittance → prioritised → appeal letter → staff submit
- **Job:** Ranks denials by chance of recovery and drafts appeal letters with clinical evidence on payer forms.
- **Trigger:** `repository_dispatch: remittance.835.denial`.
- **Systems:** 835 remittance, EHR clinicals, payer forms; writes appeal packages.
- **Steps:**
  1. [judge] score "probability this denial is recoverable" → work queue order.
  2. [prompt] appeal letter on the payer form with cited clinicals.
  3. [judge] noul "Does every clinical claim cite the record?"
  4. [approval] staff review and submit.
- **Human gate:** submission.
- **Verify:** citation check; overturn rate tracked.
- **Success number:** Waystar (vendor): "90% reduction in time to create 100 appeal packages", "40% higher denial overturn rate".
- **Evidence:** https://www.waystar.com/blog-powerful-ai-examining-results-of-waystar-altitudeai-in-rcm/ · https://www.anthropic.com/news/healthcare-life-sciences
- **Skills:** appeal-writer (new).
- **Ready today?** Needs clearinghouse + EHR connectors.

### 4. `referral-fax-intake` — Inbound fax → referral? → patient match → EHR task
- **Job:** Identifies which inbound faxes are referrals, matches patient and order type, and creates the EHR task; chases missing information.
- **Trigger:** `repository_dispatch: fax.received` (folder watch).
- **Systems:** fax/OCR, EHR/MPI; writes referral tasks and outreach.
- **Steps:**
  1. [run] OCR.
  2. [judge] noul "Is this document a referral?"
  3. [judge] noul "Does it match this patient record?" — uncertain → staff.
  4. [prompt] extract order and check coverage criteria; list missing items.
  5. [approval] staff confirm before the EHR write when match is not in the yes band.
- **Human gate:** uncertain patient match (wrong-chart risk).
- **Verify:** per-field confidence with the source span.
- **Success number:** Notable (vendor, partner-attributed): faxed orders into Epic "97% reduction in turnaround time"; "Referrals that used to take weeks are now taking less than two days".
- **Evidence:** https://www.notablehealth.com/use-cases/access · https://newsroom.clevelandclinic.org/2026/09/09/cleveland-clinic-partners-with-luminai-to-transform-health-system-operations
- **Skills:** doc-extractor (new).
- **Ready today?** Needs fax/OCR ingest and EHR connector.

### 5. `coding-confidence-gate` — Suggested codes auto-dropped only above a proven threshold
- **Job:** Suggests codes for finished charts, auto-accepts only codes above a threshold proven on past charts, and sends the rest to coders.
- **Trigger:** `repository_dispatch: chart.discharged_not_final_coded`.
- **Systems:** EHR documentation, coding system; writes suggested codes.
- **Steps:**
  1. [prompt] suggest codes with supporting note spans.
  2. [judge] per code, noul "Is this code supported by the documentation?"
  3. [run] yes → auto-drop; else → coder queue.
  4. [approval] initially coders review every auto-dropped code; relax only after precision is shown.
- **Human gate:** coder review of everything outside the proven band.
- **Verify:** ongoing sample audit of auto-dropped codes.
- **Success number:** Solventum: "95% of accepted confident codes are no longer reviewed, and around 15% of final code sets no longer require review".
- **Evidence:** https://www.solventum.com/en-us/home/health-information-technology/resources-education/case-studies/ai-in-healthcare-autonomous-medical-coding-journey/ · https://www.digitalhealthnews.com/epic-unveils-agent-factory-expands-ai-tools-at-himss26
- **Skills:** code-suggester (new).
- **Ready today?** Needs EHR + encoder connectors.

### 6. `trial-eligibility-prescreen` — Records pre-screened per criterion for investigators
- **Job:** Pre-screens patients against each trial criterion with a justification, and sends full, partial and borderline matches to investigators.
- **Trigger:** `schedule: nightly` over new patients, or `workflow_dispatch` when a trial opens.
- **Systems:** EHR (inside the firewall), trial criteria; writes a match list.
- **Steps:**
  1. [run] structured-code prefilter.
  2. [judge] per criterion, noul "Does the record meet this criterion?" with a justification.
  3. [run] classify full / partial / borderline with a tunable threshold.
  4. [approval] investigator reviews all partial, complete and borderline matches.
- **Human gate:** every eligibility decision.
- **Verify:** auditable per-criterion justification; threshold chosen on a labelled set.
- **Success number:** Cleveland Clinic: "96% accurate… across nine prespecified domains"; TRIAGE at threshold 0.13: sensitivity 98.7%, specificity 97.6%; TrialGPT: "40% less time screening".
- **Evidence:** https://consultqd.clevelandclinic.org/ai-can-unlock-ehr-data-to-determine-trial-eligibility · https://ascopubs.org/doi/10.1200/OP-26-00076
- **Skills:** criteria-matcher (new).
- **Ready today?** Needs EHR data access inside the firewall (self-hosted runner via `runs-on:`).

**Rejected:** payer auto-denial (clause 5: 2026 state laws require a licensed clinician; Cigna's "1.2 seconds on each case") · patient-facing symptom triage without a nurse (clause 5) · ambient scribe as a template (clause 7: Intermountain found "no significant benefits to… provider productivity"; also EHR-native, not a workflow).

---

## research

### 1. `literature-screen` — Recall-first screening with humans confirming every inclusion
- **Job:** Runs a saved search, screens titles and abstracts against inclusion criteria, extracts data, and has a person confirm each inclusion and field.
- **Trigger:** `schedule: weekly Mon 06:00` (living review) or `workflow_dispatch` with a protocol.
- **Systems:** PubMed/Semantic Scholar APIs, reference manager; writes a screening table.
- **Steps:**
  1. [run] search; dedupe against already-screened IDs (state).
  2. [judge] per paper, noul "Meets inclusion criteria?" tuned for recall.
  3. [prompt] extract data fields with the quoted sentence.
  4. [approval] reviewer marks each extraction correct / incomplete / wrong.
- **Human gate:** inclusion and every extracted value.
- **Verify:** quoted source span per field.
- **Success number:** JHEOR: extraction accuracy "72.93%", "No data were hallucinated", precision low — so recall-first with human confirmation. Elicit claims "save up to 80% of time" (vendor).
- **Evidence:** https://jheor.org/article/165173-evaluating-ai-performance-in-systematic-literature-reviews-for-heor-a-case-study · https://support.elicit.com/en/articles/14759154-systematic-reviews-in-elicit
- **Skills:** lit-screener (new).
- **Ready today?** Yes (public APIs via webfetch).

### 2. `metric-gated-experiment-loop` — Overnight keep/discard experiments on one metric
- **Job:** Runs experiments overnight on one file against a fixed eval, keeping changes only when the metric improves.
- **Trigger:** `workflow_dispatch` (human agrees the run tag) then `schedule: nightly`.
- **Systems:** research repo, fixed eval harness; writes commits and a results table.
- **Steps:**
  1. [approval] agree scope and metric at setup (the only gate).
  2. [prompt] edit the one allowed file; commit.
  3. [run] run the eval with a time limit; parse the metric.
  4. [run] improved → keep; else `git reset`; log to results.tsv.
  5. [judge] noul "Did the change touch the eval or anything off-limits?" → fail the run.
- **Human gate:** setup, and morning review before anything leaves the branch.
- **Verify:** read-only eval harness; metric computed by `run:`, never the model.
- **Success number:** autoresearch: about 100 experiments while the human sleeps; Ginkgo's schema-gated loop cut cost 40% ($422/g vs $698/g).
- **Evidence:** https://github.com/karpathy/autoresearch/blob/master/program.md · https://www.prnewswire.com/news-releases/ginkgo-bioworks-autonomous-laboratory-driven-by-openais-gpt-5-achieves-40-improvement-over-state-of-the-art-scientific-benchmark-302680619.html
- **Skills:** experimenter (new).
- **Ready today?** Yes (state + run steps); GPU needs a labelled runner.

### 3. `cited-research-report` — Question → parallel research → report with citations verified
- **Job:** Produces a research report on a question, fanning out sub-researchers and checking every citation exists and supports its sentence.
- **Trigger:** `workflow_dispatch` or `repository_dispatch: research.requested` (ticket).
- **Systems:** web, internal docs; writes a report to Drive/Notion.
- **Steps:**
  1. [approval] plan and scope ("Preview the plan, adjust the scope").
  2. [prompt] parallel sub-researchers with objective, output format, boundaries.
  3. [prompt] lead synthesises.
  4. [run] fetch every cited URL; [judge] per citation, noul "Does this source support the sentence?"
  5. [approval] requester accepts.
- **Human gate:** plan and final acceptance.
- **Verify:** citation existence and support check (Deloitte refunded a report with fabricated references).
- **Success number:** no cited success number; Anthropic warns multi-agent runs use "about 15× more tokens than chats".
- **Evidence:** https://www.anthropic.com/engineering/multi-agent-research-system · https://fortune.com/2025/10/07/deloitte-ai-australia-government-report-hallucinations-technology-290000-refund/
- **Skills:** citation-checker (new).
- **Ready today?** Yes.

### 4. `grant-compliance-check` — Draft proposal checked against the funding call before submission
- **Job:** Turns a funding call into a checklist and checks the draft, budget and biosketch against it; the PI certifies originality.
- **Trigger:** `repository_dispatch: proposal.draft_ready` or `schedule: T-14 days` before deadline.
- **Systems:** funding opportunity, draft files; writes a compliance report.
- **Steps:**
  1. [prompt] extract requirements from the call into a checklist.
  2. [run] format checks (page limits, fonts, sections).
  3. [judge] per requirement, noul "Does the draft satisfy this requirement?"
  4. [run] count PI's applications this year against NIH's cap of 6.
  5. [approval] PI certifies originality and AI-use disclosure.
- **Human gate:** submission and certification.
- **Verify:** deterministic format checks; per-requirement judgment.
- **Success number:** grant pros say AI is "cutting the grant writing time in half for many"; NIH saw PIs submit "more than 40 distinct applications" in one round, hence the cap.
- **Evidence:** https://grants.nih.gov/grants/guide/notice-files/NOT-OD-25-132.html · https://www.instrumentl.com/blog/grant-writing-ai-report
- **Skills:** checklist-auditor (new).
- **Ready today?** Yes for files in a folder.

### 5. `notebook-entry-check` — Lab notes → structured entry → completeness check → scientist signs
- **Job:** Turns protocols and notes into a structured notebook entry, checks it for completeness, and waits for the scientist's signature.
- **Trigger:** `repository_dispatch: eln.entry.submitted`.
- **Systems:** ELN (Benchling), uploaded notes/CoAs; writes the entry draft and a check report.
- **Steps:**
  1. [prompt] draft structured entry from notes.
  2. [judge] per required field, noul "Is this field present and consistent with the source?"
  3. [prompt] list gaps for the scientist.
  4. [approval] scientist signs (21 CFR Part 11 e-signature).
- **Human gate:** signature.
- **Verify:** completeness check; source spans.
- **Success number:** Benchling: "Saves scientists up to 2 weeks spent transforming complex data with Data Entry Assistant".
- **Evidence:** https://www.benchling.com/blog/ai-tools-for-the-modern-lab · https://claude.com/customers/benchling
- **Skills:** doc-extractor (new).
- **Ready today?** Needs an ELN connector.

### 6. `new-paper-alert` — Weekly relevant-paper digest for a project
- **Job:** Watches new publications for a project's topics and sends only relevant ones with a one-line why.
- **Trigger:** `schedule: weekly Fri 07:00`.
- **Systems:** arXiv/PubMed/Semantic Scholar; writes a digest; state keeps seen IDs.
- **Steps:**
  1. [run] fetch new papers for saved queries.
  2. [judge] per paper, noul "Is this relevant to the project's stated questions?"
  3. [prompt] one-line relevance with the abstract sentence quoted.
  4. [run] send digest.
- **Human gate:** none (read-only digest).
- **Verify:** quoted sentence per item.
- **Success number:** no cited number.
- **Evidence:** https://www.undermind.ai · https://consensus.app/home/about-us/
- **Skills:** lit-screener (new).
- **Ready today?** Yes.

**Rejected:** fully automated paper generation and submission (clause 5: Sakana's system edited its own script to extend its timeout; funders are capping volume) · AI peer review (clause 5: "Reviewers remain responsible"; NSF bans uploading proposals) · research chat assistant (clause 1).

---

## education

### 1. `essay-second-reader` — AI second score, human on disagreement
- **Job:** Scores each essay alongside the human reader and sends it to a second human only when the two disagree by more than the set margin.
- **Trigger:** `repository_dispatch: application.essay.scored` (after the human read).
- **Systems:** admissions system, rubric; writes AI score and escalation flag.
- **Steps:**
  1. [judge] score the essay on the rubric (0–12).
  2. [run] compare with the human score.
  3. [run] difference >2 → second human reader.
  4. [approval] the human remains the primary reader.
- **Human gate:** every decision stays with human readers.
- **Verify:** disagreement rate monitored; bias checks across groups.
- **Success number:** Virginia Tech: "saving at least 8,000 hours", decisions "a month sooner", 57,622 applications.
- **Evidence:** https://apnews.com/article/ai-chatgpt-college-admissions-essays-87802788683ca4831bf1390078147a6f · https://easyclass.ai/blog/ai-grading-accuracy-research
- **Skills:** rubric-scorer (new).
- **Ready today?** Yes on exported essays (`wfx judge` batch).

### 2. `feedback-before-release` — Rubric feedback drafted, hidden until the teacher approves
- **Job:** Drafts rubric-aligned feedback on a folder of student work and keeps it hidden until the teacher approves each comment.
- **Trigger:** `repository_dispatch: assignment.due_passed` or `workflow_dispatch` on a folder.
- **Systems:** Google Docs/Classroom, rubric; writes staged comments.
- **Steps:**
  1. [prompt] feedback per document against the rubric.
  2. [judge] noul "Does each comment reference the student's text?"
  3. [approval] teacher reviews each comment before students see it.
  4. [prompt] class-level pattern summary for the teacher.
- **Human gate:** release to students; grades stay with the teacher (LLM grading shows "consistent proportional bias").
- **Verify:** comment anchored to a quoted passage.
- **Success number:** no cited number.
- **Evidence:** https://www.briskteaching.com/give-feedback · https://easyclass.ai/blog/ai-grading-accuracy-research
- **Skills:** feedback-writer (new).
- **Ready today?** Needs Google Classroom/Docs connector (Drive MCP covers read).

### 3. `transfer-credit-evaluation` — Transcript → courses → equivalency table → registrar exceptions
- **Job:** Extracts courses from transfer transcripts, maps them via the equivalency table, and sends only unmapped ones to the registrar.
- **Trigger:** `repository_dispatch: transcript.received`.
- **Systems:** transcript PDFs, equivalency table, SIS; writes credit evaluations.
- **Steps:**
  1. [prompt] extract courses and grades.
  2. [run] deterministic lookup in the equivalency table.
  3. [judge] per unmapped course, choice "closest equivalent / none" with confidence.
  4. [approval] registrar decides every non-table mapping.
- **Human gate:** any credit not from the table.
- **Verify:** table lookups are exact; extraction spot-checked.
- **Success number:** Stony Brook "auto-processes 60% of transcripts" (third-party report).
- **Evidence:** https://gradpilot.com/news/which-colleges-use-ai-2025 · https://apnews.com/article/ai-chatgpt-college-admissions-essays-87802788683ca4831bf1390078147a6f
- **Skills:** doc-extractor (new).
- **Ready today?** Yes on PDFs; SIS write needs a connector.

### 4. `deadline-nudges` — Targeted student reminders with escalation to an advisor
- **Job:** Sends targeted reminders before FAFSA, registration and hold deadlines, answers replies, and escalates to an advisor.
- **Trigger:** `schedule: daily 10:00` against the deadline calendar.
- **Systems:** SIS (holds, registration), SMS/email; writes messages and advisor tasks.
- **Steps:**
  1. [run] students with an open item before a deadline.
  2. [prompt] message per segment.
  3. [judge] per reply, noul "Does this need an advisor?" → task.
  4. [approval] new message types approved by the office that owns them.
- **Human gate:** new campaigns (Georgia State says "no" to off-mission messages).
- **Verify:** completion rate of the target action.
- **Success number:** Georgia State: +6% FAFSA filed by the priority deadline; advisor meetings +29%.
- **Evidence:** https://mainstay.com/blog/expanding-to-persistence/
- **Skills:** none beyond the platform.
- **Ready today?** Needs SIS + SMS connectors.

### 5. `iep-draft-deidentified` — De-identified goal drafts for the IEP team
- **Job:** De-identifies student data, drafts IEP goals from assessments, and hands them to the IEP team, parents included.
- **Trigger:** `workflow_dispatch` when an IEP meeting is scheduled.
- **Systems:** assessment data; writes draft goals.
- **Steps:**
  1. [run] de-identify inputs.
  2. [prompt] draft goals tied to the student's assessment data.
  3. [judge] noul "Is each goal specific to this student's data rather than boilerplate?"
  4. [approval] IEP team decides.
- **Human gate:** the IEP team (IDEA requires an individualised, collaborative plan).
- **Verify:** boilerplate check; PII check.
- **Success number:** no outcome number; "more than half" of special-ed teachers already use AI for IEPs and 15% write the whole plan with it.
- **Evidence:** https://www.govtech.com/education/k-12/ai-gains-ground-in-special-ed-raises-legal-and-ethical-concerns · https://support.khanacademy.org/hc/en-us/articles/14799047733645-What-teacher-tools-are-available-on-Khanmigo
- **Skills:** pii-scrubber (new).
- **Ready today?** Yes.

### 6. `lesson-plan-grounded` — Lesson plan from the approved curriculum, teacher approves
- **Job:** Drafts a lesson from the school's approved curriculum content and standards, and the teacher edits and approves.
- **Trigger:** `schedule: weekly Thu 15:00` for next week's units, or `workflow_dispatch`.
- **Systems:** curriculum repository, standards list; writes lesson docs.
- **Steps:**
  1. [run] fetch next unit and standards.
  2. [prompt] draft lesson, quiz, exit ticket from approved content only.
  3. [judge] noul "Does each activity map to a listed standard?"
  4. [approval] teacher approves.
- **Human gate:** teacher "in the driving seat".
- **Verify:** standards mapping check.
- **Success number:** Oak Aila early survey: a majority of teachers report time saved (the EEF RCT has no result yet).
- **Evidence:** https://www.thenational.academy/blog/how-is-aila-impacting-teacher-lesson-planning-practices-workload-and-expertise-early-insights · https://educationendowmentfoundation.org.uk/projects-and-evaluation/projects/aila-teacher-choices-trial
- **Skills:** none beyond the platform.
- **Ready today?** Yes with curriculum in a folder.

**Rejected:** "AI-written?" detectors used for discipline (clause 4: a 1% false-positive rate means ~223,500 essays wrongly flagged) · AI-only grading (clause 5: bias) · student-facing AI tutor (clause 7: Khanmigo trial found students used it "not much" with no extra benefit).

---

## public-sector

### 1. `consultation-theming` — Public consultation responses → themes → reviewer-checked dashboard
- **Job:** Finds themes in consultation responses, has experts refine them, maps every response, and lets reviewers correct each mapping.
- **Trigger:** `repository_dispatch: consultation.closed` (batch).
- **Systems:** response export; writes theme assignments and a dashboard.
- **Steps:**
  1. [prompt] propose themes per question.
  2. [approval] experts check and refine themes.
  3. [judge] per response × theme, noul "Does this response express this theme?" (`wfx judge` batch).
  4. [approval] reviewers accept/add/remove themes per response.
  5. [run] dashboard for policy makers.
- **Human gate:** theme set and per-response corrections (democratic legitimacy).
- **Verify:** agreement vs two human coders.
- **Success number:** i.AI Consult: F1 0.76; exact theme match 60% vs 62% human–human agreement; used on 38 consultations; target "75,000 days of analysis every year… £20 million".
- **Evidence:** https://ai.gov.uk/blogs/evaluating-consult-an-ai-tool-for-enhanced-public-consultation-analysis/ · https://www.gov.uk/government/news/government-built-humphrey-ai-tool-reviews-responses-to-consultation-for-first-time-in-bid-to-save-millions
- **Skills:** theme-coder (new).
- **Ready today?** Yes on CSV exports.

### 2. `meeting-minutes` — Recording → minutes in the council template → officer approves
- **Job:** Transcribes a meeting and drafts minutes or case notes in the required template for the officer to edit and approve.
- **Trigger:** `repository_dispatch: recording.uploaded`.
- **Systems:** recording, template; writes the minutes document.
- **Steps:**
  1. [run] transcribe.
  2. [prompt] summarise into the template (decisions, actions, owners).
  3. [judge] per action item, noul "Is this action stated in the transcript?"
  4. [approval] officer edits and approves.
- **Human gate:** publication of statutory minutes / filing of case notes.
- **Verify:** each action links to a transcript timestamp.
- **Success number:** Minute "reduced the time taken to complete minutes for a 60 minute meeting by one hour" (22 authorities); Justice Transcribe "50% reduction in note-taking time", 1,000 probation officers.
- **Evidence:** https://ai.gov.uk/knowledge-hub/tools/minute/ · https://ai.gov.uk/our-work/frontline-services/
- **Skills:** minute-writer (new).
- **Ready today?** Yes (needs a transcription CLI in the runner).

### 3. `permit-precheck` — Applicant plans checked against code before a reviewer sees them
- **Job:** Checks submitted plans against zoning and code rules and returns a fix list to the applicant before the examiner's review.
- **Trigger:** `repository_dispatch: permit.application.submitted`.
- **Systems:** permit system, code/zoning rules; writes a pre-check report.
- **Steps:**
  1. [run] extract plan data (dimensions, setbacks, uses).
  2. [judge] per rule, choice "complies / does not / cannot determine".
  3. [prompt] applicant-facing fix list with the rule cited.
  4. [approval] plans examiner makes the decision.
- **Human gate:** the permit decision.
- **Verify:** each finding cites the code section.
- **Success number:** LA County: 54% reduction in processing time; Austin: about 50% time saving on zoning (customer quotes).
- **Evidence:** https://www.archistar.ai/aiprecheck/ · https://www.civcheck.ai/
- **Skills:** rule-checker (new).
- **Ready today?** Needs the permit system connector and plan parsing.

### 4. `caseworker-policy-answer` — Caseworker question ticket → cited policy answer
- **Job:** Answers caseworker policy questions from verified federal, state and local policy with citations, flagging uncertain ones to a supervisor.
- **Trigger:** `repository_dispatch: casework.question.created` (internal ticket).
- **Systems:** policy library (MCP), case system; writes an answer on the ticket.
- **Steps:**
  1. [prompt] answer grounded only in the policy library, with citations.
  2. [judge] noul "Is the answer supported by the cited sections?" — uncertain → supervisor.
  3. [approval] supervisor check when flagged (Caddy "routes responses for human checks when needed").
  4. [run] post the answer.
- **Human gate:** the caseworker decides the case; the supervisor clears flagged answers.
- **Verify:** citation support.
- **Success number:** Nava RCT (125 caseworkers): accuracy "about 40%" better; Citizens Advice Caddy "halved the time taken to respond".
- **Evidence:** https://www.navapbc.com/case-studies/evaluating-ai-assistive-chatbot-caseworkers · https://codeforamerica.org/news/anthropic-partnership/
- **Skills:** policy-answerer (new).
- **Ready today?** Needs the policy corpus loaded; otherwise yes.

### 5. `planning-record-extract` — Scanned planning documents → structured records
- **Job:** Extracts map features and text, including handwriting, from historic planning documents for a person to correct and export.
- **Trigger:** `repository_dispatch: document.uploaded` (scan folder).
- **Systems:** scans; writes records to the planning data platform.
- **Steps:**
  1. [prompt] extract features and text into the schema.
  2. [judge] per field, noul "Is this value legible and correctly read?"
  3. [approval] user reviews and corrects before export.
  4. [run] export.
- **Human gate:** export to the national record.
- **Verify:** per-field confidence.
- **Success number:** Extract: "up to 2 hours per record reduced to 2 minutes"; launched to every council in England.
- **Evidence:** https://mhclgdigital.blog.gov.uk/2026/06/17/extract-is-here-ai-powered-planning-data-for-every-council-in-england/ · https://ai.gov.uk/our-work/planning/
- **Skills:** doc-extractor (new).
- **Ready today?** Yes on scans in a folder.

### 6. `foia-pre-redaction` — Records request → flagged exemptions → officer redacts
- **Job:** Flags sensitive data and likely exemptions in collected records so the FOIA officer can redact faster and meet the deadline.
- **Trigger:** `repository_dispatch: foia.request.logged`.
- **Systems:** records collection, exemption rules; writes pre-redacted drafts.
- **Steps:**
  1. [run] collect responsive documents; deadline set in state.
  2. [judge] per passage, choice "exemption type / none" tuned for recall.
  3. [approval] officer reviews and adds redactions.
  4. [run] release with correspondence; deadline reminders.
- **Human gate:** release (over- and under-redaction are both harms).
- **Verify:** officer decisions feed calibration.
- **Success number:** no cited number.
- **Evidence:** https://www.opexustech.com/product/foiaxpress/ai-assist/
- **Skills:** redaction-flagger (new).
- **Ready today?** Yes on document folders.

**Rejected:** automated fraud adjudication or penalties (clause 5: Michigan MiDAS ~93% of auto-adjudicated cases wrong; Robodebt A$1.8B settlement) · chatbot replacing a human helpline (clause 5: NEDA's "Tessa" gave dieting advice) · benefit eligibility decided by a classifier alone (clause 5).

---

## general

### 1. `inbox-triage-labels` — Every new email labelled; nothing deleted
- **Job:** Labels each new email by type and whether it needs action, so the inbox sorts itself without losing anything.
- **Trigger:** `schedule: every 15 min` (or Gmail push webhook).
- **Systems:** Gmail/Outlook; writes labels only.
- **Steps:**
  1. [run] new messages since last run (state).
  2. [judge] choice "label", noul "Needs a reply from me?" (Fyxer's reply-decision model).
  3. [run] apply labels; never archive in the uncertain band; never delete.
  4. [run] daily count of "needs reply" to the owner.
- **Human gate:** none (labels are reversible); deletion is out of scope.
- **Verify:** owner relabels become calibration data.
- **Success number:** n8n "Auto Categorise Outlook Emails with AI" 38,584 views; no cited accuracy number.
- **Evidence:** https://n8n.io/workflows/2454 · https://openai.com/index/fyxer/
- **Skills:** none beyond the platform.
- **Ready today?** Yes with the Gmail MCP.

### 2. `reply-drafts` — Drafts for emails that need a reply, never sent
- **Job:** Writes a draft reply in the user's voice for every email that needs one and leaves it in Drafts.
- **Trigger:** `schedule: every 15 min`.
- **Systems:** Gmail/Outlook, past sent mail; writes drafts.
- **Steps:**
  1. [judge] noul "Needs a reply?"
  2. [prompt] draft using thread history and the user's past replies.
  3. [judge] noul "Does the draft commit to anything (price, date, yes/no) not already stated by me?" → mark for attention.
  4. [run] save as draft.
- **Human gate:** the user sends.
- **Verify:** commitment check.
- **Success number:** Fyxer: "53% Of AI-generated drafts accepted as written"; n8n draft auto-responder (#2271) 54,727 views.
- **Evidence:** https://openai.com/index/fyxer/ · https://n8n.io/workflows/2271
- **Skills:** reply-drafter (new).
- **Ready today?** Yes with the Gmail MCP (`create_draft`).

### 3. `meeting-actions` — Transcript → decisions and owners → tasks → approved follow-up
- **Job:** Extracts decisions and action items from a meeting, creates tasks, and drafts the follow-up email for approval.
- **Trigger:** `repository_dispatch: recording.transcript_ready`.
- **Systems:** transcript, task tool (Notion/Asana), email; writes tasks and a draft.
- **Steps:**
  1. [prompt] extract decisions, owners, dates.
  2. [judge] per item, noul "Is this stated in the transcript?"
  3. [run] create tasks.
  4. [approval] follow-up email to external attendees.
- **Human gate:** external email.
- **Verify:** each item links to a transcript timestamp.
- **Success number:** no cited measurement; n8n #2328 has 30,956 views and a Reddit user calls the notetaker "the automation I appreciate most".
- **Evidence:** https://n8n.io/workflows/2328 · https://www.reddit.com/r/automation/comments/1lwewj2/
- **Skills:** minute-writer (new).
- **Ready today?** Yes with Gmail MCP; task tool needs a connector.

### 4. `attachment-filing` — Email attachments renamed and filed
- **Job:** Saves invoices, receipts and contracts from email to the right Drive folder with a consistent name.
- **Trigger:** `schedule: hourly`.
- **Systems:** Gmail, Google Drive; writes files and a log sheet.
- **Steps:**
  1. [run] new messages with attachments.
  2. [judge] choice "document type (invoice / receipt / contract / other)".
  3. [prompt] extract date, counterparty, amount for the filename.
  4. [run] file; uncertain type → "to-sort" folder.
- **Human gate:** none (copies only, original stays).
- **Verify:** log sheet of every file moved.
- **Success number:** receipts and invoices into Sheets saved "8 - 10 hours every month" (Reddit); a top-popular template on Make, Zapier and Power Automate.
- **Evidence:** https://www.reddit.com/r/automation/comments/1jzp9yz/whats_the_best_automation_youve_built_that/ · https://n8n.io/workflows/1897
- **Skills:** none beyond the platform.
- **Ready today?** Yes with Gmail + Google Drive MCPs.

### 5. `morning-brief` — Calendar, priority email and open asks in one message
- **Job:** Sends one morning message with today's meetings, emails that need a reply, and asks you're waiting on.
- **Trigger:** `schedule: weekdays 07:00`.
- **Systems:** calendar, email, task tool; writes one message (WhatsApp/Slack/email).
- **Steps:**
  1. [run] gather calendar, unread "needs reply", overdue tasks.
  2. [prompt] prioritise with a reason per item.
  3. [run] deliver.
- **Human gate:** none (read-only).
- **Verify:** every item links to its source.
- **Success number:** anecdote: "Took 30 mins to set up, saves me 10x that weekly".
- **Evidence:** https://www.reddit.com/r/automation/comments/1l2voxr/ · https://github.com/anthropics/knowledge-work-plugins/tree/main/sales
- **Skills:** none beyond the platform.
- **Ready today?** Yes with Gmail MCP; calendar needs a connector.

### 6. `status-digest` — Weekly project update from tracker changes, approved before execs
- **Job:** Writes the weekly status update from what actually changed in the tracker and repo, and holds it for approval before it reaches execs.
- **Trigger:** `schedule: Fri 15:00`.
- **Systems:** Jira/Linear/GitHub; writes the update to email/Slack.
- **Steps:**
  1. [run] tickets moved, PRs merged, dates slipped this week.
  2. [prompt] draft update: done, next, risks.
  3. [judge] per claim, noul "Does a ticket or PR support this?"
  4. [approval] owner before sending to execs or clients.
- **Human gate:** send to stakeholders.
- **Verify:** every claim links to a ticket/PR.
- **Success number:** no cited measurement (Notion ships it as "Status update agents").
- **Evidence:** https://www.notion.com/product/agents · https://n8n.io/workflows/3969
- **Skills:** report-writer (new).
- **Ready today?** Yes for GitHub; Jira/Linear need connectors.

**Rejected:** generic "AI agent chat" (clause 1: 780,399 views, no trigger or output contract) · personal assistant that sends email and accepts invites for you (clause 5) · link summariser to a notes app (clauses 2, 3: nothing acts on it) · auto-delete of email (clause 5).
