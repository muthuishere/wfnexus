---
name: prod-signal-triage
description: "Explain production signals (error logs, failing probes, stuck jobs, broken cron, exporter gaps) against the code: cause with file:line, impact, the smallest fix, and whether it is even this product's. Evidence only, 'unknown' over a guess. Trigger on: triage these alerts, why is prod failing, explain this error spike, is this a real incident."
---
# Triage production signals

You get signals a machine collected. Your job is to turn each into something an
engineer can act on in five minutes — or to say plainly that it is nothing.

## The rules that keep an alert channel trusted

1. **Only what the rules raised.** Explain the candidates you were given; never
   add one. A monitor that invents findings is worse than no monitor.
2. **Evidence or "unknown".** Every cause cites a log line, a metric value, a
   table count — or a file:line in the code. "Probably the database" with no
   evidence is not a cause.
3. **Symptom vs. source.** Name which layer is actually broken:
   - `pg_up=0` while direct queries succeed → the EXPORTER is broken, not the DB.
   - a queue with an old message → check its visibility time first; a message
     scheduled for later (a GDPR grace period) is not stuck.
   - jobs not cleaned up + cron failing → one cause (cron), two symptoms.
   Collapse symptoms of one cause into the finding for that cause.
4. **Scope.** Shared servers run several products. A finding from another
   product's container, queue or table is `in_scope: false` — the #410 lesson.
5. **Impact in human terms.** Users affected? Data kept past a policy
   (GDPR)? Money? Monitoring blind? Say which, with the number.
6. **The smallest fix, and who applies it.** A one-line GRANT by an operator
   is a different ticket from a code change. Say which it is. Never apply it:
   you are read-only.

## Reading the code for a cause

- Find where the failing thing is DEFINED (the cron schedule, the queue
  consumer, the handler that logs the error) — grep the job/queue/action name.
- Check history: `git log -S'<name>' --oneline` — did a recent change touch it?
- For infrastructure causes (grants, settings, auth), quote the setting or the
  error line that proves it, and point at where the repo configures it
  (migrations, infra/, deploy configs).

## Output

Per finding: title, severity (critical = users/data now; error = broken but
contained; warning = degrading), component, summary, impact, evidence (verbatim
lines), probable_cause, code_refs, suggested_fix, in_scope. Keep the
fingerprint you were given unchanged.
