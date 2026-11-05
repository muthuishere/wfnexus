# ADR 0026 — WhatsApp only: subscriptions, conversations and the default workflows

- **Status:** proposed
- **Date:** 2026-11-05
- **Amends:** ADR 0024. Teams, Slack and email are **out of scope**. The owner's call on 2026-11-05 was "we will do only whatsapp".

## Decision

The platform pieces stay generic. The only adapter and the only templates are for WhatsApp, through `apl`.

1. **Subscriptions are data.** Each subscription has these fields:
   - name, chat, events, interval;
   - target workflow and version;
   - skills, instructions, response policy (`draft | approval | auto | none`), memory policy;
   - owner and status.

   Status moves through `validating → active ⇄ paused`, then `deleted`. A failed check moves it to `error`.

   Changes apply without a restart. They are made with `wfx sub add|update|ls|pause|resume|rm|validate`, `/api/subscriptions` or a UI view.
2. **One inbox read per account per tick.**
   - The tick goes from 10 s to 5 s.
   - Each subscription keeps its own read position.
   - A new subscription starts from when it is switched on. Reading older messages is opt-in, and those messages never get a reply.
3. **Routing.**
   - Each message becomes one event that carries its chat, sender, message id and delivery id.
   - Delivery mode is `fanout`, `exclusive` (the default for subscriptions that reply) or `observe`.
   - The pair (subscription, message id) is unique, so a message seen twice starts no new run.
   - Messages we sent ourselves (`FromMe`) are dropped.
4. **Conversations.**
   - The key is (account, chat). Each conversation has its own memory.
   - Its state moves `active → waiting → resolved → closed` and can be reopened.
   - Runs for one conversation go one at a time.
5. **The `start:` step.** It has four modes:
   - `wait: false` starts the child and moves on;
   - `wait: true` waits for the child's result;
   - `continue: event` finishes now and handles the result later as a `workflow.completed` event in the same conversation;
   - a list starts several workflows at once.

   The parent run is recorded. Limits: 5 levels deep, 20 children per run.
6. **Sends.** Each send has a delivery id. A send that may or may not have gone through is marked `uncertain` and is never resent automatically.

## Default workflows

| template | does |
|---|---|
| `whatsapp-subscription-manager` | add, update, pause, resume, delete, list, validate (checks the chat exists, the login can read it and the target workflow exists) |
| `whatsapp-monitor` | health of the shared inbox poll: how far behind the read position is, error state |
| `whatsapp-chat-handler` | receive → find the conversation → load memory → classify → pick a skill or `start:` a workflow → reply through the notifier → save memory |
| `whatsapp-notifier` | sends under the response policy; result is `sent`, `failed` or `uncertain` |
| `whatsapp-escalation` | sends an alert, waits for `#code ack`, then escalates after a timeout |
| `whatsapp-digest` | scheduled summary of chosen chats: decisions, asks and owners |

## Order of work

1. Subscriptions, including the API, CLI and UI.
2. Inbox mode, shared poll, routing and no duplicates.
3. Conversations and the `start:` step.
4. The six templates, with a live run on a test group.
5. Password sign-in (ADR 0025).
