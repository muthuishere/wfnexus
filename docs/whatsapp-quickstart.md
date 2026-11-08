# WhatsApp quickstart

From nothing to a bot that answers in a test group and is watched, in five
steps. The background is in ADR 0027 (hooks and MCP) and ADR 0028 (service
modules: the whatsapp-group-monitor). The adapter contract is in
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

## 2. Install pingpong and expose it as a hook

```sh
wfx new whatsapp-sender      # copies keep the template's name; the others start it by name
wfx new pingpong
# whatsapp-sender sends on the DEFAULT channel (channels.default: whatsapp),
# so the channel's sendGap paces every op
wfx expose hook pingpong     # prints the hook URL and its token once
wfx env set PINGPONG_HOOK_TOKEN   # paste the token (stored sealed)
```

## 3. Run the monitor and register a watch

```sh
wfx module install https://github.com/muthuishere/wfnexus --ref main   # or your fork
wfx module config whatsapp-group-monitor
wfx module start whatsapp-group-monitor --account biz   # [--runs-on <worker label>]
wfx module watch add whatsapp-group-monitor --account biz \
  '{"name":"pingpong-test","chat":"<test-group-jid>@g.us","hook":"<hook url>","tokenEnv":"PINGPONG_HOOK_TOKEN"}'
```

One process per account reads every watched chat once per tick, holds a chat's
messages until it is quiet for 3s, and posts the batch to the watch's hook. A
new watch is picked up on the next tick. History from before the monitor
started is never delivered.

## 4. Watch a run

Send `ping` in the group. Within the interval:

```sh
wfx module ls                    # running, last heartbeat, restarts
wfx runs --workflow pingpong     # the run; its child is the whatsapp-sender run
```

The group gets `pong #1`. Send `ping` again and the reply is `pong #2`,
because memory is kept per conversation.

Then keep it watched:

```sh
wfx new whatsapp-monitor   # set owner_chat, WA_CHANNEL, STORE_PROBE
```

Every 5 minutes it compares the WhatsApp store with what wfnexus has heard,
and checks the monitor service. If the channel is connected but deaf, or the
service is silent or crashed, it alerts your chat once per window.

## 5. Expose frontdesk over MCP

```sh
wfx new load-context && wfx new frontdesk   # set HANDOFF_WORKFLOWS, MIN_CONFIDENCE
wfx expose mcp frontdesk                    # prints the token once, plus the client config
```

Any MCP client can now call the `frontdesk` tool with
`{"message": {"chat": "…", "id": "…", "from": "…", "text": "…"}, "subscription": {"instructions": "…"}}`.
It answers from that chat's memory, or hands off to a workflow or a person. To
answer real chats with it, expose it as a hook and add a watch the same way as
pingpong. A watch's delivery carries no `subscription`, so load-context's
default response policy applies: set it to `approval`, so every reply waits
for you until you trust it.

## The default workflows

| template | does |
|---|---|
| `whatsapp-sender` | one send, policy-checked; `sent`, `failed` or `uncertain`; never resends uncertain |
| `pingpong` | the reference handler: memory → reply via the sender → save |
| `frontdesk` | answers from memory and instructions; hands off when unsure (embeds `load-context`) |
| `whatsapp-monitor` | catches a connected-but-deaf channel, plus a silent or crashed monitor service |
