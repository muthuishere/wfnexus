# ADR 0023 — Channels are commands, and a chat reply resolves only from an allow list

- **Status:** accepted
- **Date:** 2026-10-20
- **Builds on:** ADR 0021 (a pause names who may answer)

## Context

ADR 0021 made a pause *announced*: a `webhook` notifier POSTs a pointer to the
pause somewhere. Three things were still missing.

1. **Nothing was sent by default.** `notifyPause` returned early when no
   notifier was registered, so on a normal install a run that paused at 02:00
   told nobody.
2. **Nothing came back.** A person who saw the pause in a chat still had to
   open the UI or run `wfx approve`. There was no way to say "yes" where the
   message arrived.
3. **Workflows could not talk.** A scheduled watch had no built-in way to send a
   message, and no way to be started by one.

The obvious build is a WhatsApp client in the server. We already run one — a
shared gateway owns the only paired device, with rules (one writer per number,
sends spaced, never retried blind) that are easy to break from a second
implementation. Email is next, then whatever comes after it.

## Decision

### 1. A channel is a COMMAND we run, not a transport we speak

A channel is a `registries.json` entry of `kind: command`:

```json
"channels": {
  "team-chat": {
    "kind": "command",
    "run": "node adapters/apl-whatsapp.js",
    "env": { "APL_WA_GROUP": "${TEAM_GROUP_JID}" },
    "inbound": true, "receiveEvery": "1m",
    "allow": ["15550100001@s.whatsapp.net"]
  }
}
```

The server runs `<run> send` with a JSON envelope on stdin, and — for an
inbound channel — `<run> receive` on a schedule with the last cursor on stdin.
Exit 0 is success; a non-zero exit is a failure whose stderr is shown on the
run. That is the whole contract (`docs/channels.md`, `docs/schemas/`), versioned
as `wfnexus.channel/v1`.

**Why not embed.** The transport is somebody else's system with somebody else's
rules. An adapter script is owned next to that system (the apl project owns its
WhatsApp adapter; `adapters/apl-whatsapp.js` is a reference), changes without a
wfnexus release, and is testable with nothing but stdin and stdout. Email is a
second script — `kind`, envelope and engine are unchanged — which is the test of
the shape: **swapping WhatsApp for email is a config edit.**

**Why not an HTTP adapter service.** It was considered and dropped: an inbound
endpoint is one more thing to authenticate and expose, while a command run by
the server has no listening surface at all. Receiving is a poll the server
starts; nothing on the outside can push into it.

A server-wide default — `channels.default` in config.yaml, overridden by
`WFX_DEFAULT_CHANNEL`, the same pattern as `model.fallback` — is where every
pause and every unaddressed `send:` goes. Notifiers (`webhook`) are unchanged
and still receive pauses.

### 2. The outbound message carries a pointer and a code, never a capability

ADR 0021's rule holds. An envelope carries the run URL (a pointer at our UI,
which authenticates) and, for a pause, a **short code** — six characters from
an unambiguous 31-letter alphabet (≈887M values), written `#a7k2qp` in the
text and kept in `pause_codes` against the run, the step **and the channel it
was sent on**.

The code is a **lookup, not a credential.** It says *which* pause a reply
means. On its own it authorizes nothing: anyone who can read the chat can read
the code. Even so it is kept narrow:

- it matches only on the channel it was sent on (`(channel, code)` is the key);
- it expires: **7 days** after it is issued, or the pause's own expiry if
  that is sooner. A pause still waiting after a week is answered in the UI or
  with `wfx approve`; the run is not affected, only the chat shortcut;
- it is **retired** the moment its pause resolves by any path (UI, CLI, chat)
  and when the step pauses again — so a reply replayed later can never answer
  a newer pause. It is issued synchronously, on the run's own goroutine, before
  the (background) send — and re-checked against the step after it is written —
  so it can never be created after the pause it names has already resolved;
- a reply is checked against every code it quotes, and the first live one on
  the channel answers;
- a collision draws a new code (`ON CONFLICT DO NOTHING`); an issued code is
  never re-pointed.

### 3. A reply resolves a pause only from an allow-listed sender

A received message that quotes a code is a candidate answer. It resolves the
pause **only if its sender id is on that channel's explicit `allow` list**:

- approval pause: `yes | y | approve | ok | lgtm` approves; `no | reject | deny
  [reason]` rejects with the reason;
- input / question pause: the text after the code is the answer; a bare `no` declines.

Every other reply — a sender not on the list, a code for a pause that is no
longer waiting, an approval reply that is neither yes nor no — is **recorded**
on the run's event stream and in `channel_messages` (outcome `recorded`) and
resolves nothing. **The default allow list is empty, so by default nothing
resolves from chat.** Turning it on is a named, per-channel act.

The resolution is an ADR 0021 `Actor`: `ID` is the sender id, `Via` is
`channel:<name>`, and it is treated as authenticated — so a step's `approvers:`
still applies, and the audit row reads `<sender> (via channel:<name>)`.

Authority is therefore **allow-listed identity + code**, never the code alone,
and never mere membership of the group.

### 4. Inbound trust is the adapter's sender id — so it is only as good as the transport

The allow list compares the `from` the adapter reports. What that is worth
depends on the transport:

- **WhatsApp** (through the shared gateway / apl / wacli): `from` is the
  sender JID the WhatsApp protocol authenticated to the paired device. Spoofing
  it means compromising the device or the adapter host, not typing a name.
- **Email**: `From:` is trivially forged. An email adapter MUST report a sender
  only after verifying it (DKIM/DMARC alignment, or a provider API that
  authenticated the mailbox) — or be configured with an empty allow list, i.e.
  send-only for answers. This is a rule for adapter authors, written into the
  contract, because wfnexus cannot check it.

Received messages are stored once per `(channel, id)`, so a re-delivered reply
cannot approve twice. A message is stored **pending** before it is acted on and
marked handled after; the cursor moves only after every message is stored; a
pending message is handled on the next poll. A crash between storing and acting
therefore re-handles a `#code yes` rather than losing it.

### 5. Messages are also inputs — and their text is UNTRUSTED

A new trigger, `on: message: [{channel, contains, match, allow}]`, starts a
workflow with the message as `input.message`. A reply that quotes a pause code
is an answer and does not also trigger.

Starting a run is authority too, so a message trigger has the same gate as an
answer: it fires only for a sender on the trigger's `allow` list, or — when that
is empty — the channel's. **Both empty ⇒ it never fires**, and the refusal is
logged and recorded (outcome `refused`). A message starts each workflow **at
most once**: a claim row keyed by `(channel, message id, workflow)` is taken in
one `INSERT … ON CONFLICT DO NOTHING` before the run is created, so a message
re-handled after a crash finds its claim and starts nothing. (The other
direction — a crash after the claim, before the run — loses that start. For a
message that may say "deploy", at-most-once is the right failure.) The run's `triggeredBy` is the
**sender id**, not `trigger:message`, so a step's `prevent_self_approval`
refuses the person who started a run approving it from the same chat.

**`input.message.text` is untrusted input from a chat.** An allow list limits
*who* can send it; it does not make the text safe. A workflow that templates it
into a `run:` command is a shell for that sender, and into a prompt is a prompt
injection. Treat it like a webhook body: pass it to a step as data, quote it,
or let a `judge` step classify it before anything acts on it.

A new step kind, `send:`, renders text through a channel with **dedup by key**
(`channel_dedup`) so a 15-minute schedule says a thing once per window. The
claim is ONE statement — `INSERT … ON CONFLICT DO UPDATE … WHERE sent_at <
cutoff RETURNING` — so two runs racing for a key cannot both send. A send that
the adapter REFUSED (non-zero exit) releases its key; one that was CUT OFF (the
timeout) keeps it, because it may have been delivered. `dry_run: true` records
what it would have sent without running the adapter. Nothing is ever retried.

### 6. What an adapter is given

- **A minimal environment**: `PATH`, `HOME`, temp dirs, locale (and the Windows
  equivalents), plus exactly the channel's `env:` — whose `${VAR}` references
  are how an operator hands one server variable to one adapter. The server's
  database URL and provider keys are never inherited.
- **Its deadline**: `WFX_CHANNEL_TIMEOUT_MS`, so it can stop and say "may have
  been delivered" before it is killed.
- **Only public attachment URLs**: `https` to an address that is not loopback,
  private, link-local or CGNAT — including an IPv4 such an address carries
  inside IPv6 (IPv4-mapped, IPv4-compatible, NAT64 `64:ff9b::/96`, 6to4
  `2002::/16`; Teredo and NAT64 local-use are refused outright) — checked by the server before any adapter runs
  (the reference adapter re-checks at fetch time and refuses redirects). A send
  step's file attachment must be inside the run's workspace.
- Its **stderr is scrubbed** before it reaches an event, a step error or a log.

## Consequences

- A fresh install with `channels.default` set announces every pause, with no
  webhook.
- The server never holds a WhatsApp (or mail) session and never speaks the
  protocol; one more transport is one more script.
- Sends on one channel are serialized in-process, optionally spaced
  (`sendGap`); a rate-limited transport should set it, and its adapter should
  still pace itself.
- Receivers, like the scheduler, are per-process: two server processes would
  poll twice (dedup makes it harmless, but wasteful). Recorded in
  `docs/not-now.md`.
- An adapter is code the server runs (with a minimal environment). It is
  configured by an operator in `registries.json`, never by a workflow.
- Pause announcements are sent in the background after the run parks, so a
  slow adapter never holds a run's slot.
- Handled messages (and their run claims) are kept 90 days; expired or retired
  pause codes 30 days.
- Received messages are visible by project: a message is in a project when it
  answered or started a run there; one that matched nothing is visible only to
  an unscoped admin.
- On SIGINT/SIGTERM the server stops taking requests and drains in-flight
  pause announcements for up to 20 seconds.
