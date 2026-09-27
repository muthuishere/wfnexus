# Recipe index — match the person's words, not the file names

| recipe | people say… | category | catalogue entry |
|---|---|---|---|
| `production-watch.md` | "tell me when payments/signups/jobs start failing", "watch prod", "the cron died silently" | debugging-ops | error-to-fix-pr, alert-investigator; house example reqsume-prod-watch |
| `alert-triage.md` | "explain the page", "what changed when the alert fired" | debugging-ops | alert-investigator |
| `ci-failure-doctor.md` | "main is red and nobody knows why", "duplicate CI issues" | debugging-ops | ci-failure-doctor |
| `dead-code-remover.md` | "delete dead code safely", "shrink the codebase" | development | code-remover |
| `docs-drift-fixer.md` | "our docs lie", "README is out of date" | development | docs-drift-fixer |
| `dependency-upgrade-repair.md` | "Dependabot PRs pile up", "we're majors behind" | development | dep-upgrade-repair |
| `pr-review-verified.md` | "review every PR, but only real findings" | development | Summary pattern 3 (no single entry) |
| `issue-triage-labelling.md` | "label and dedupe our issues" | product-design | issue-intake-triage |
| `inbound-ticket-triage.md` | "route support tickets", "draft replies for support" | support | ticket-triage-route + grounded-reply-draft |
| `weekly-kpi-narrative.md` | "the Monday KPI report", "numbers disagree between sources" | data | weekly-kpi-narrative |
| `invoice-expense-exceptions.md` | "review expenses against policy", "catch duplicate invoices" | finance | expense-policy-review + ap-invoice-coding |
| `contract-nda-triage.md` | "NDAs wait two weeks for legal", "can sales sign this" | legal | nda-triage |

No match → say so and use `workflow-author`; the catalogue
(`docs/scenarios/top-6-by-category.md`) has 102 more shapes to borrow from.

Every recipe has the same sections: Use when · Not when · Evidence · Questions
(one at a time, default in brackets, toy checks) · Skeleton · questions.yaml ·
Gates · Success number · Bar check.
