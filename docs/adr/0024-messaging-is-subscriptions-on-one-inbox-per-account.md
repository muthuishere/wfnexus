# ADR 0024 — Messaging is subscriptions on one inbox per account; a handler hands work to other workflows

- **Status:** superseded by ADR 0028 (subscriptions and the router removed in #104; conversations remain)
- **Date:** 2026-11-05
- **Builds on:** ADR 0023 (channels are commands; replies need an allow list)
- **Spike:** `docs/spikes/2026-11-05-whatsapp-inbox/` (FINDINGS.md, probe.mjs)
- **Order:** WhatsApp first. Teams, Slack and email follow with the same model.

## Context

The owner wants "beautiful" messaging templates: watch any WhatsApp chat or
group, answer in it with a workflow that remembers that conversation, and let
that workflow start other workflows ("as many as we like"), all added and
removed while the server runs. Teams follows WhatsApp.

What exists today (checked on main, 2026-11-05):

| piece | where | limit for this use |
|---|---|---|
| channel = a command with `send` / `receive` | `docs/channels.md`, `internal/engine/channels.go` | — |
| WhatsApp adapter | `adapters/apl-whatsapp.js` | **one group per channel** (`APL_WA_GROUP`, must be `@g.us`); no DMs |
| `on: message:` trigger, allow list | `internal/workflow/triggers.go:60` | matches channel + text; a message carries **no chat id** |
| poll interval | `receiveEvery` (default 1m), loop ticks every **10 s** (`channels.go:86`) | 25 s works, but rounds up to the next 10 s tick (30 s) |
| workflow memory, on by default | `workflow/memory.go:145` (`EffectiveMemory` → `On: true`), `engine/memory.go` | one memory **per workflow**, not per conversation |
| live catalog reload | `engine/watch.go` (PR #65, race fixed in #67) | registry edits apply without restart |
| `uses:` | `workflow/jobs.go:56` | expands another workflow's steps INTO this run, not an independent run |
| a step can reach the server | `WFX_API` in every step's env (`engine/env.go:119`), `wfx run <wf> -i k=v -f` | works today, but nothing records the parent → child link |

Spike results (read-only, `whatsapp:biz`, 57 chats: 11 groups, 46 direct):
listing chats takes 12 ms; one `messages list --after <t>` across **every**
chat takes 12–15 ms; three polls 25 s apart cost < 50 ms in total. One
`wacli` store already holds every chat, so one read per account per poll is
enough — there is no reason to run one poller per chat.

## Decision

1. **One inbox channel per account, not per chat.** The WhatsApp adapter gets
   an inbox mode (`APL_WA_GROUP=*` or unset): `receive` reads every chat after
   the cursor in one call, DMs included. Each received message gains two
   optional contract fields: `chat` (the transport's conversation id: a group
   JID, a DM JID, later a Teams chat id) and `chatName`. `send` already takes
   `target`; in inbox mode `target` may be any chat id, DMs included. Old
   adapters that don't set `chat` keep working unchanged.

2. **A subscription is data, not a workflow edit.** `registries.json` gets a
   `subscriptions` map, which hot-reloads like the rest of the catalog:

   ```json
   "subscriptions": {
     "sales-group": {
       "channel": "wa-biz", "chat": "1203…@g.us",
       "workflow": "whatsapp-chat-handler", "project": "sales",
       "allow": ["9198…@s.whatsapp.net"], "skills": ["pricing"],
       "match": "", "paused": false
     }
   }
   ```

   A received message that matches a subscription (channel + chat [+ match],
   sender on the subscription's `allow`, else the channel's) starts that
   workflow with `input.message` and `input.subscription`. `wfx sub
   add|ls|pause|resume|rm` and a Subscriptions view in the UI edit it; nothing
   needs a restart. A subscription's `skills` can only narrow what the target
   workflow already permits. `on: message:` triggers keep working for static
   cases. **Empty allow = nobody**, exactly as in ADR 0023.

3. **Memory is per conversation when a run has one.** Memory stays on by
   default for every workflow. When a run starts from a message, its memory
   key is `(workflow, project, channel, chat)`, so one chat never reads
   another's history; the workflow-wide lessons still load. `memory:
   {scope: workflow}` opts out. Every memory entry stays editable (separate
   change: `PATCH /api/memory/{id}`, `POST /api/workflows/{name}/memory`, edit
   in the UI).

4. **A workflow starts another workflow as its own run, and can wait for it.**
   A new step kind, `start:` (not `run:`, which is already the shell step):

   ```yaml
   - id: investigate
     start: { workflow: incident-investigation, input: { summary: "{{steps.triage.output.summary}}" }, wait: true }
   ```

   The child is an independent run: its own state, memory, approvals and
   history, with `parentRun` recorded so the UI links both ways. With
   `wait: true` the parent pauses until the child finishes and gets its
   output; without it, it fires and forgets. Depth and fan-out are capped
   (defaults: depth 5, 20 children per run). `uses:` stays what it is, inlining
   steps.

5. **Polling stays; the tick gets finer.** The receive loop ticks every 5 s
   instead of 10 s, so a 25 s `receiveEvery` really is 25 s. A push path
   (`apl serve` already relays live messages to loopback subscribers) is a
   later optimisation. The poll is cheap enough that it isn't needed to ship.

6. **Templates, WhatsApp first:**

   | template | what it does |
   |---|---|
   | `whatsapp-chat-handler` | reply in the chat using per-chat memory and the subscription's skills; a confidence gate routes unsure replies to a person; may `start:` other workflows |
   | `whatsapp-group-digest` | scheduled summary of chosen groups (decisions, asks, owners) sent to one chat |
   | `whatsapp-escalation` | send an alert, wait for `#code ack`, escalate to the next person after a timeout |
   | `whatsapp-intake` | turn messages matching a pattern into typed records (lead, ticket, order) and confirm back |

   Then the same four for Teams through Microsoft Graph (`apl call ms:<label>`).
   The spike confirmed that reading chats works with the existing login
   (`/me/chats` about 1 s, messages about 0.5 s). Graph refuses `$filter`
   on `createdDateTime`, so the Teams adapter pages newest-first and stops at
   the cursor. Sending to Teams needs `ChatMessage.Send`, which hasn't been
   granted or tested yet.

## Consequences

- One poll per account covers every chat, so adding the 50th subscription
  costs a map lookup, not a process.
- The channel contract grows by two optional fields (`chat`, `chatName`);
  `docs/channels.md` and the receive schema change, but old adapters stay valid.
- Inbox mode reads DMs. The allow list is the gate: an unknown sender starts
  nothing, and the text is untrusted (ADR 0023 rules unchanged).
- A sent message must never start a run: inbox mode drops `FromMe`, as the
  group mode does now.
- `start:` adds a run→run edge to the store (one nullable `parent_run_id`
  column, paired PG/SQLite migrations).
- Not decided here: username/password login and simpler user and role
  management (ADR 0025).
