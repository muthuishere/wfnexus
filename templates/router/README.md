# router

Routes, and does nothing else. Not WhatsApp-specific: any event object works.

- `classify` — first matching rule in `routes.json` (`kind`, `chatType`,
  `chat`, `source`, `fromOwner` equal; `text` a regex). Exit 3 = no route
  (outcome `ok: no route…`), exit 2 = broken table (the run fails).
- `route` — `start:` the matched workflow as its own child run with
  `message: {{ json .Input.event }}` — a value that is exactly
  `{{ json <path> }}` keeps its type, so the handler gets an object.

**Input:** `event` (object, required), `subscription` (object, passed through).
**Output:** the start step's `{runId, workflow, status}`.

Route vs embed: routing starts another run (own memory, own failure, linked as
a child). frontdesk shows embedding (`uses:`), where another workflow's steps
run inside the caller's run.

Need judgement instead of rules? Put a `judge:` step before `classify` and
match on its answer.
