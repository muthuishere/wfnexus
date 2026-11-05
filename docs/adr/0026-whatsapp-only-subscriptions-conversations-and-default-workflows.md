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
   - The interval is one of `30s`, `1m` (default) or `2m`. The existing 10 s tick is enough for that, so it stays.
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

Every workflow keeps memory. It is on by default and stored per conversation when a run comes from a chat.

| template | does |
|---|---|
| `whatsapp-subscription-manager` | add, update, pause, resume, delete, list, validate (checks the chat exists, the login can read it and the target workflow exists) |
| `whatsapp-monitor` | turns every WhatsApp event into one wfnexus event (see "WhatsApp coverage" below), drops our own echoes, and hands each new event to the router |
| `whatsapp-sender` | the mirror of the monitor: text, quote-reply, attachment, voice, reaction, edit and delete of our own message; records each sent id so the monitor ignores it; result is `sent`, `failed` or `uncertain` |
| `router` (common, not WhatsApp-specific) | matches an event to its subscriptions (`fanout`, `exclusive` or `observe`) and starts each target workflow as its own run |
| `pingpong` | reference handler: receives a message, reads its conversation memory, replies through `whatsapp-sender`, and saves what was said. It proves the whole loop end to end. |

Further handlers (chat handler, escalation, digest) are built later on the same pieces.

## WhatsApp coverage (monitor and sender are symmetric)

The transport is `wacli` behind `apl serve`. The live feed is `/subscribe`, with an HMAC-signed push. A store poll on the subscription's interval (30s, 1m or 2m) is the backfill and safety net. There is one writer per number.

| kind | monitor emits | sender does |
|---|---|---|
| chat | `chat`, `chatName`, `chatType: dm\|group` | target any DM or group JID |
| text | `text`, `mentions[]` | text, with `@` mentions |
| reply / thread | `replyTo: {id, sender, text}` (`ReplyToID`, `ReplyToSenderJID`, `ReplyToDisplay`) | `--reply-to` + `--reply-to-sender` |
| attachment | `media: {kind, mime, name, size}`, file fetched on demand (`/messages/{id}/media`), 50 MB cap | file, image, voice note, sticker |
| reaction | `message.reaction` with `{targetId, emoji}` | react to a message id |
| edit / delete | `message.updated` / `message.deleted` from the live feed | edit or delete our own message |
| own messages | dropped: `FromMe` **and** the id is in the sender's sent-id ledger; a `FromMe` message we didn't send (typed on the phone) is kept as `fromOwner` | each sent id is written to the ledger before it is reported `sent` |

The rules are taken from OpenClaw's WhatsApp channel (Baileys; source read 2026-11-05) and from its open issues:

- **Echo:** OpenClaw keeps a sent-id cache in memory for 20 minutes. Our ledger is in the database, so a restart can't turn our own reply into a new message.
- **Groups need a mention by default:** a group subscription starts a run only when we're mentioned or a message replies to one of ours. Setting `activation: always` turns that off. A DM goes through the allow list.
- **Backlog is never answered:** messages from before the subscription started, or from while the connection was down, are kept as history but never get a reply. OpenClaw issue #834: on startup it messaged about 20 contacts with a configuration message.
- **Per-chat lane and debounce:** messages that arrive within 3 s in one chat become one run. Runs for one chat go one at a time.
- **The listener can be connected but deaf:** this is OpenClaw's most-reported failure (#34741, #45171, #45387 and others). `whatsapp-monitor` checks that events are actually arriving, separately from "socket connected". It raises an alert if the live feed is silent while the store poll is still finding new messages.
- **Fill the gaps after a reconnect:** after a reconnect, the store poll fills in everything since the last read position. That gap is still open in OpenClaw (#50093).
- **No silent media loss:** a send is `sent` only after WhatsApp confirms it. A send with an attachment checks that the attachment was actually included (OpenClaw #61726).
- **Where we go further than OpenClaw:** OpenClaw drops reactions, edits and deletes. We emit them as events.

What to reuse:
- herdr-hook's `wadrv` already turns quotes, reactions, media and our own ids into fields. Port that.
- The retired messenger's `whatsapp_apl.go` already used the `apl serve` push.
- The current `adapters/apl-whatsapp.js` (one group, text only) is replaced.

## Order of work

1. Subscriptions, including the API, CLI and UI.
2. Inbox mode, shared poll, routing and no duplicates.
3. Conversations and the `start:` step.
4. The five workflows above, with `pingpong` run live on a test group.
5. Password sign-in (ADR 0025).
