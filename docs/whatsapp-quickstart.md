# WhatsApp quickstart

From nothing to a bot that answers in a test group and is watched, in five
steps. The background is in ADR 0026 (subscriptions, conversations, default
workflows) and ADR 0027 (hooks and MCP). The adapter contract is in
[channels.md](channels.md).

## 1. Create the channel

`registries.json`:

```json
{
  "channels": {
    "whatsapp": {
      "kind": "command",
      "run": "node adapters/apl-whatsapp.js",
      "env": { "APL_WA_LABEL": "biz" },
      "allow": ["<your-jid>@s.whatsapp.net"],
      "timeoutSec": 150
    }
  }
}
```

Leave `APL_WA_GROUP` unset to use inbox mode, where one channel serves every
DM and group. Check it with `wfx channels` and `wfx channels receive whatsapp`.

## 2. Set `self`

Group subscriptions only answer when someone mentions or replies to **us**.
wfnexus asks the adapter's `self` verb; if that fails, list the ids yourself:

```json
"self": ["15550100000@s.whatsapp.net", "15550100000:12@s.whatsapp.net"]
```

`wfx sub show <name>` prints a warning while self is unknown. `whatsapp-monitor`
reports the same thing as `unknown-self`.

## 3. Add a subscription: pingpong on a test group

```sh
wfx new whatsapp-sender      # copies keep the template's name; the others start it by name
wfx new pingpong
# whatsapp-sender sends on the DEFAULT channel (channels.default: whatsapp),
# so the channel's sendGap paces every op

wfx sub add pingpong-test --channel whatsapp --chat <test-group-jid>@g.us \
  --workflow pingpong --allow <your-jid>@s.whatsapp.net \
  --policy auto --activation always --interval 30s
```

`--allow` is the owner only. An empty list means nobody, and `*` means
everyone. Messages sent before the subscription became active are treated as
backlog and are never answered.

To do the same from a chat or an agent, use `whatsapp-subscription-manager`:
`{"action":"add","name":"pingpong-test","channel":"whatsapp","chat":"…","workflow":"pingpong","allow":"…"}`.

## 4. Watch a run

Send `ping` in the group. Within the interval:

```sh
wfx sub show pingpong-test       # the delivery: started, with its run id
wfx runs --workflow pingpong     # the run; its child is the whatsapp-sender run
```

The group gets `pong #1` as a quote-reply. Send `ping` again and the reply is
`pong #2`, because memory is kept per conversation. In the UI, the run page shows
the `send` step linked to its child sender run, and that run's `deliver` output
(`{"status":"sent","id":…}`).

Then keep it watched:

```sh
wfx new whatsapp-monitor   # set owner_chat, WA_CHANNEL, STORE_PROBE
```

Every 5 minutes it compares the WhatsApp store with what wfnexus has heard. If
the channel is connected but deaf, or a subscription is failing, it alerts your
chat once per window.

## 5. Expose frontdesk over MCP

```sh
wfx new load-context && wfx new frontdesk   # set HANDOFF_WORKFLOWS, MIN_CONFIDENCE
wfx expose mcp frontdesk                    # prints the token once, plus the client config
```

Any MCP client can now call the `frontdesk` tool with
`{"message": {"chat": "…", "id": "…", "from": "…", "text": "…"}, "subscription": {"instructions": "…"}}`.
It answers from that chat's memory, or hands off to a workflow or a person. To
answer real chats with it, subscribe it the same way as pingpong:
`wfx sub add desk --workflow frontdesk --policy approval …`. Start with
`approval`, so every reply waits for you until you trust it.

## The default workflows

| template | does |
|---|---|
| `whatsapp-sender` | one send, policy-checked; `sent`, `failed` or `uncertain`; never resends uncertain |
| `pingpong` | the reference handler: memory → reply via the sender → save |
| `frontdesk` | answers from memory and instructions; hands off when unsure (embeds `load-context`) |
| `router` | classifies an event and `start:`s the workflow that owns it |
| `whatsapp-monitor` | catches a connected-but-deaf channel, plus failing subscriptions and unknown self |
| `whatsapp-subscription-manager` | add, update, pause, resume, rm, ls and validate through the API, reading each change back |
