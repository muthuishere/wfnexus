---
name: product-cartographer
description: "Map a product repository the way a new engineer and a new ops lead both need it: what the products are, the user journeys, where production signals live, and — most of all — the RECURRING MANUAL WORK people do around it (from runbooks, task files, issues, retros, specs). Evidence with file:line only. Trigger on: understand this product, map this repo, what does this codebase do, what work could we automate here."
---
# Map the product — and the work around it

The deliverable is not a tour of the code. It is the list of things PEOPLE do,
again and again, to keep this product running and growing — because that is
what a workflow can take off them.

## Read in this order (stop reading a source once it stops adding facts)

1. **What it is:** README, `docs/project-overview*`, `CLAUDE.md` / `AGENTS.md`,
   product/marketing copy. One paragraph: who pays, for what.
2. **What it does:** route tables, handlers, queue/action names, cron schedules,
   webhooks. One line per user-facing capability.
3. **How it runs:** deploy configs (Kamal, k8s, compose), health endpoints,
   logging, metrics, alerting, the database and its failure tables.
4. **The work around it — the part that matters:**
   - runbooks (`docs/runbooks/`), incident/RCA docs, retros, checklists;
   - task runners (`Taskfile*.yml`, `Makefile`, `package.json` scripts) — each
     manual target is a job somebody runs by hand;
   - CI workflows, especially `workflow_dispatch`-only ones (manual) or broken;
   - issues: `gh issue list --state all --limit 200 --json title,labels,createdAt`
     — group by recurring theme; labels like `sre-alert`, `bug`, `ceo-backlog`;
   - specs `in-progress` — work stuck half-done is work somebody keeps carrying;
   - existing automation (`.wfx/workflows/`, agent skills, cron jobs, bots) —
     so you do not propose what already runs.

## Output

- **products:** name, one-line purpose, entry points (file:line), domain.
- **journeys:** the 5–10 things a user does, each with the code that serves it.
- **ops_surfaces:** where logs, metrics, health, DB failure signals live.
- **recurring_work:** each item = what people do, how often (evidence: dates,
  counts, a runbook), who does it, what system it touches, the evidence path.
  This list is the point; aim for 10–25 real items.
- **existing_automation:** what already runs, so nothing is proposed twice.

Say "not found" rather than infer. A recurring-work item with no evidence
(no runbook, no repeated issue, no task target) does not go in the list.
