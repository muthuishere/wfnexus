# frontdesk

The engine's front desk. Answers anyone about this wfnexus engine — what
workflows exist, what ran, what failed, what is waiting on whom, how to do X —
from a read-only view of `$WFX_API`. When it is not sure, it hands off; it
never improvises. It starts, approves and changes nothing on the engine itself.

| step | what |
|---|---|
| `ctx.gather` | **embedded** from `load-context` with `uses:`: writes `context.json` in this run's workspace |
| `engine` | GET-only snapshot of `$WFX_API` → `engine.json`: workflows, run counts, recent and failed runs, runs waiting on a person; `error` when the API cannot be read |
| `answer` | one agent turn (read only) over `engine.json`, `context.json` and this chat's memory → `{reply, confidence, handoff_to, handoff_workflow, diagnose_workflow, diagnose_run, reason}` |
| `decide` | "why did X fail" → diagnose (12); no chat → answered in the run (20); confident → reply (0); a workflow on `HANDOFF_WORKFLOWS` → hand off (10); else → a person (11) |
| `reply` | `start: whatsapp-sender` (`wait: true`) with the watch's responsePolicy (default `draft`) |
| `diagnose` → `explain` → `reply-diagnosis` | `start: retrospective` (`wait: true`) focused on the workflow/run; its checked, cited findings become the reply |
| `handoff` | `start: <workflow>` with `continue: event`: this run ends; the child's completion returns to the chat as `workflow.completed` |
| `person` → `send-approved` | approval gate showing the draft; approve sends it, reject leaves the chat to the person |

**Two ways in:** a watched WhatsApp message (the monitor's hook body: `message`, `messages`, `watch`, `chat`)
gets its reply in the chat. A manual run or MCP call with `question` gets the
answer as the run's output (`answer.reply`, or `explain` for a diagnosis);
nothing is sent anywhere.

**Read-only by default:** asked to start, approve or change something, it
hands the request to a person instead of doing it.

**Embed vs route:** `uses: load-context` puts that workflow's steps inside this
run (shared workspace, one history). `start:` makes a separate run.

**Fill:** copy `load-context`, `whatsapp-sender` and `retrospective` too; set
`HANDOFF_WORKFLOWS`, `MIN_CONFIDENCE`, and `WFX_TOKEN` when the API needs one;
add your skills to `answer`.

**Over MCP:** `wfx expose mcp frontdesk` makes it a tool (`question`, or
`message` + `watch`) that any agent can call.
