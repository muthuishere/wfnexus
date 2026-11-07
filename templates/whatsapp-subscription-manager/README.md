# whatsapp-subscription-manager

`wfx sub` as a workflow, so a chat or an agent can manage subscriptions.

**Input:** `action` (add | update | pause | resume | rm | ls | validate),
`name`, and for add/update any of the subscription fields: `channel`, `chat`,
`workflow`, `version`, `events`, `interval`, `skills`, `instructions`,
`responsePolicy`, `memory`, `mode`, `activation`, `allow`, `owner`. List fields
are comma-separated strings. For `ls`, `channel` narrows the list.

**Rules:** `add` needs `channel`, `chat`, `workflow` and an explicit `allow`.
An empty allow list means nobody can start a run, so the workflow refuses it
instead of creating a subscription that never fires. `rm`, and any change that
lets everyone (`*`) start an auto-replying workflow, wait for approval.

**Output** (`apply` stdout): `{action, name, ok, verified, status, problem,
warning, subscription, subscriptions, error}`. Every change is read back from
the API, and `verified` is true only when the server shows the expected status.
A refusal or a mismatch fails the run.

**Expose it** (ADR 0027):

```sh
wfx expose mcp whatsapp-subscription-manager    # an MCP tool for agents
wfx expose hook whatsapp-subscription-manager   # POST /hooks/<project>/whatsapp-subscription-manager
```

```sh
curl -X POST "$WFX/hooks/<project>/whatsapp-subscription-manager?wait=1" \
  -H "Authorization: Bearer $HOOK_TOKEN" \
  -d '{"action":"pause","name":"pingpong-test"}'
```
