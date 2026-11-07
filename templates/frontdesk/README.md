# frontdesk

Just answers. When it is not sure, it hands off; it never improvises.

| step | what |
|---|---|
| `ctx.gather` | **embedded** from `load-context` with `uses:`: writes `context.json` in this run's workspace |
| `answer` | one agent turn over `context.json` + this chat's memory → `{reply, confidence, handoff_to, handoff_workflow, reason}` |
| `decide` | confident (≥ `MIN_CONFIDENCE`) → reply; names a workflow on `HANDOFF_WORKFLOWS` → hand off; else → a person |
| `reply` | `start: whatsapp-sender` (`wait: true`) with the subscription's responsePolicy |
| `handoff` | `start: <workflow>` with `continue: event`: this run ends; the child's completion returns to the chat as `workflow.completed` |
| `person` → `send-approved` | approval gate showing the draft; approve sends it, reject leaves the chat to the person |

**Embed vs route:** `uses: load-context` puts that workflow's steps inside this
run (shared workspace, one history). `start:` makes a separate run.

**Fill:** copy `load-context` and `whatsapp-sender` too; set
`HANDOFF_WORKFLOWS` and `MIN_CONFIDENCE`; add your skills to `answer`.

**Over MCP:** `wfx expose mcp frontdesk` makes it a tool (`message` +
`subscription` input) that any agent can call.
