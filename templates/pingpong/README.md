# pingpong

The smallest end-to-end proof: a message in a chat → a whatsapp-group-monitor
watch → this workflow's hook → this run → `whatsapp-sender` → a reply in the chat, and the next message
remembers this one.

1. `reply` — reads this conversation's memory and answers `pong #N` (N = one
   more than the highest pong remembered). Non-text events (reactions, edits)
   are not answered.
2. `send` — `start: whatsapp-sender` with `wait: true`, quote-replying to the
   message and passing `input.watch.responsePolicy` (default `draft`: never auto-sends).
3. `save` — writes `pong #N — answered <who>, sent <id>` as the run outcome;
   per-conversation memory carries it to the next run.

**Output** (`save` stdout): `{heard, said, status, id}`.

Try it on a test group (see docs/whatsapp-quickstart.md):

```sh
wfx new whatsapp-sender && wfx new pingpong
wfx expose hook pingpong          # URL + token; store the token: wfx env set PINGPONG_HOOK_TOKEN
wfx module start whatsapp-group-monitor --account biz
wfx module watch add whatsapp-group-monitor --account biz \
  '{"name":"pingpong-test","chat":"<test-group-jid>","hook":"<url>","tokenEnv":"PINGPONG_HOOK_TOKEN","responsePolicy":"auto"}'
```

Send `ping` twice in the group: you get `pong #1`, then `pong #2`.
