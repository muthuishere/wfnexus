# Spike — one WhatsApp inbox per account; Teams read path (2026-11-05)

The question was whether one poll can cover every chat of an account cheaply
enough to run every 25 s, and what Teams needs beyond that. The ADR this
feeds is `docs/adr/0024-messaging-is-subscriptions-on-one-inbox-per-account.md`.

All reads were read-only. Only counts and timings were recorded; no message
text, names or ids.

## WhatsApp (`apl with whatsapp:biz --read-only -- wacli --json …`)

`node docs/spikes/2026-11-05-whatsapp-inbox/probe.mjs biz 3 25`:

```
chats list: 12 ms, 57 chats { group: 11, dm: 46 }
first poll: 12 ms, cursor set (no history emitted)
poll 1: 15 ms, 0 new (0 inbound) across 0 chats {}
poll 2: 13 ms, 0 new (0 inbound) across 0 chats {}
poll 3: 14 ms, 0 new (0 inbound) across 0 chats {}
```

An earlier read of `messages list --after 2026-11-05T00:00:00Z --asc` (no
`--chat`) returned that day's 8 messages from 3 different chats in 30 ms.

- `messages list` without `--chat` reads **every** chat. Each message carries
  `ChatJID`, `ChatName`, `SenderJID`, `SenderName`, `FromMe`, `Text`,
  `Timestamp`, `MediaType` and more. That is enough to route by chat and sender.
- Reads come from the local store that `apl serve` keeps synced (`apl serve
  status`: three handles, each with a live syncer). So a read never touches
  the WhatsApp network, which is why it takes about 15 ms.
- The adapter's cursor rule (the anchor's timestamp minus 1 s, drop what has
  been seen) works across chats unchanged.
- **Gap:** the reference adapter refuses a target that isn't a group
  (`groupOf` requires `@g.us`), so it can't read or answer DMs.
- **Gap:** the received-message contract has no chat id, because one channel
  meant one group.
- **Gap:** the engine's receive loop ticks every 10 s
  (`internal/engine/channels.go:86`), so `receiveEvery: 25s` really fires
  every 30 s.

`apl serve` also relays live messages to loopback subscribers, a push path
that would make the poll unnecessary. The poll costs so little that push can
wait.

## Teams (`apl call ms:<label> GET https://graph.microsoft.com/v1.0/…`)

There is no `apl teams` command. Teams goes through Microsoft Graph, the same
way `templates/daily-brief/sources.py` reads it.

| call | result |
|---|---|
| `/me/chats?$top=10&$orderby=lastMessagePreview/createdDateTime desc&$expand=lastMessagePreview` | 1.06 s, 10 chats (group, meeting, oneOnOne) |
| `/me/chats/{id}/messages?$top=5` | 0.5–0.65 s. System events come back as `messageType: unknownFutureValue`; real ones as `message`, with `from.user.{id, displayName}` and an **html** body |
| `…/messages?$filter=createdDateTime gt …` | **400**: Graph does not allow that filter. Page newest-first and stop at the cursor instead |
| `/me/chats/getAllMessages/delta` | **412 PreconditionFailed**: not available to this delegated login |
| `/me/joinedTeams` → `/teams/{id}/channels` → `…/messages?$top=5` | 1 team, 9 channels, channel messages readable (1.5 s) |

What Teams adds over WhatsApp:
- One call per chat. Poll recently active chats first, using
  `lastMessagePreview`.
- Turn the html body into text.
- Skip system events.
- Sending needs `ChatMessage.Send`. That scope hasn't been granted or tested,
  and sending was out of scope for a read-only spike.
