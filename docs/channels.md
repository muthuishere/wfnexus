# Channel adapter contract — `wfnexus.channel/v1`

A **channel** is how wfnexus sends messages and, optionally, receives them
(ADR 0023). wfnexus does not speak any transport: a channel is a **command** the
server runs. This file is the contract an adapter author implements; the JSON
Schemas are in [`docs/schemas/`](schemas/).

| file | what it describes |
|---|---|
| `schemas/channel-send-stdin.schema.json` | the envelope `send` reads |
| `schemas/channel-send-stdout.schema.json` | what `send` may print |
| `schemas/channel-receive-stdin.schema.json` | the request `receive` reads |
| `schemas/channel-receive-stdout.schema.json` | what `receive` must print |

Reference adapter: [`adapters/apl-whatsapp.js`](../adapters/apl-whatsapp.js)
(WhatsApp groups through the apl CLI). Test fixture: the fake adapter in
`apps/api/internal/engine/fakeadapter_test.go`.

## Configuration

`registries.json`:

```json
{
  "channels": {
    "team-chat": {
      "kind": "command",
      "run": "node adapters/apl-whatsapp.js",
      "env": { "APL_WA_GROUP": "${TEAM_GROUP_JID}", "APL_WA_LABEL": "biz" },
      "inbound": true,
      "receiveEvery": "1m",
      "allow": ["15550100001@s.whatsapp.net"],
      "sendGap": "10s",
      "timeoutSec": 150
    }
  }
}
```

| field | meaning |
|---|---|
| `kind` | `command` (the only kind) |
| `run` | the command line; the verb is appended. Runs through the platform shell, from the directory holding `registries.json` |
| `env` | extra environment for the adapter; `${VAR}` expands from the server's environment, so addresses and tokens stay out of the file |
| `target` | optional default address, passed through as `target` |
| `inbound` | poll `receive` every `receiveEvery` (default `1m`) |
| `allow` | sender ids whose replies may resolve a pause. **Empty (default) = none.** |
| `sendGap` | minimum time between sends on this channel; sends are always one at a time |
| `timeoutSec` | per invocation (default 60) |
| `enabled` | `false` switches it off |

`config.yaml` picks the server-wide default (pauses, and any `send:` that names
no channel); `WFX_DEFAULT_CHANNEL` overrides it:

```yaml
channels:
  default: team-chat
```

## `<run> send`

**stdin** — one JSON object:

```json
{
  "version": "wfnexus.channel/v1",
  "type": "pause",
  "channel": "team-chat",
  "text": "nightly-watch is waiting (approval) at step ship:\n...\n\nReply \"#a7k2 yes\" or \"#a7k2 no <reason>\".\nOpen: https://wfx.example.com/runs/6f1c…",
  "runUrl": "https://wfx.example.com/runs/6f1c…",
  "code": "a7k2",
  "pauseKind": "approval",
  "workflow": "nightly-watch", "project": "ops", "runId": "6f1c…", "stepId": "ship",
  "dedupKey": "pause:6f1c…:ship",
  "target": "",
  "attachment": { "path": "/tmp/report.pdf", "filename": "report.pdf" },
  "at": "2026-10-20T09:00:00Z"
}
```

- `type`: `pause` (a run stopped), `send` (a `send:` step or `wfx send`), `notice`.
- `text` is complete and ready to post. Adapters should not rewrite it.
- `runUrl` is a **pointer**. Nothing in an envelope resolves anything.
- `code` (pause only) is what a reply quotes. It is not a credential.
- `attachment` has a local `path` **or** a `url`, never both. Optional.
- `target` is opaque to wfnexus: a group, a mailbox. Empty ⇒ the adapter's own default.

**stdout** — optional: `{"id": "<transport message id>"}`.

**exit** — `0` means sent. Anything else is a failure: wfnexus shows the
**stderr** on the run and does **not** retry. If the adapter cannot tell whether
the message went out (a timeout, a lost response), it must fail and say so on
stderr — a duplicate in a chat is worse than an error.

## `<run> receive`

**stdin:**

```json
{ "version": "wfnexus.channel/v1", "channel": "team-chat", "after": "3EB0C4…", "target": "" }
```

`after` is the cursor this adapter returned last time; `""` on the very first
poll. On the first poll an adapter should return **no history** and a cursor at
the newest message, so turning a channel on does not replay its past into
triggers.

**stdout:**

```json
{
  "messages": [
    {
      "id": "3EB0C5…",
      "from": "15550100001@s.whatsapp.net",
      "name": "Ops lead",
      "text": "#a7k2 yes",
      "at": "2026-10-20T09:03:11Z",
      "attachments": [ { "url": "", "filename": "log.txt", "mime": "text/plain" } ]
    }
  ],
  "cursor": "3EB0C5…"
}
```

- Oldest first. Leave out messages the server itself sent.
- `id` is the transport's message id. wfnexus stores each `(channel, id)` once,
  so returning a message twice is harmless.
- `from` is what the allow list is matched against, exactly.
- `cursor` is persisted (only after every message is stored) and handed back as
  `after`. Empty ⇒ keep the previous one.

**exit** — non-zero is a failure (e.g. the transport's syncer is down). The
error is kept and shown by `wfx channels`; the cursor does not move.

## What wfnexus does with a received message

1. Stored in `channel_messages` (once).
2. If the text contains `#<code>` of a pause:
   - sender **on** `allow`: approval → `yes|y|approve|ok|lgtm` approves,
     `no|reject|deny [reason]` rejects; input → the rest of the text is the
     answer. Recorded as `<from> (via channel:<name>)`; a step's `approvers:`
     still applies.
   - sender **not** on `allow` (or the pause is no longer waiting): recorded on
     the run, resolves nothing.
3. Otherwise, every workflow whose `on: message:` matches (channel, `contains`,
   `match` regex) starts with `input.message = {channel, id, from, name, text, at, attachments}`.

## Adapter author rules

- **The sender id must be one the transport authenticated.** WhatsApp's sender
  JID is. An email `From:` is not: an email adapter must verify it (DKIM/DMARC,
  or a provider API that authenticated the mailbox) before reporting it, or the
  channel must keep an empty `allow` list.
- Never retry a send. Fail loudly instead.
- Respect the transport's rate limits yourself; `sendGap` is a backstop.
- Read nothing from wfnexus but stdin and your environment. Write nothing but
  stdout (the result) and stderr (diagnostics).
