# whatsapp-sender

The one way a workflow speaks on WhatsApp. Every other default workflow sends
through it with `start: {workflow: whatsapp-sender, wait: true}`.

**Input** (all strings): `chat` (required), `text`, `replyTo` + `replyToSender`,
`mentions`, `media` + `mediaKind` (file|image|voice|sticker), `reaction` +
`reactTo`, `edit`, `delete`, `responsePolicy` (draft|approval|auto|none,
default auto), `key` (idempotency; default = hash of the request).

**Policy:** `auto` sends · `approval` pauses at step `approve` with the text
shown · `draft` records the draft in the ledger, sends nothing · `none` skips.

**Output** (`deliver` stdout, one JSON object):
`{status: sent|failed|uncertain|drafted|skipped|already, id, action, chat, key, error}`.
`failed` and `uncertain` also fail the run with the reason.

**Never twice:** a `pending` ledger row is written before the adapter runs; a
key that is pending, uncertain or sent is never sent again (`already`). Only
`failed` (the transport said no) may be retried.

**Fill:** `WA_ADAPTER` (e.g. `node adapters/apl-whatsapp.js`, verb `send`
appended, envelope per docs/channels.md), `WA_CHANNEL`, optional
`WA_SENDER_LEDGER`.

Note: it runs the adapter directly because the `send:` step's envelope does not
yet carry `replyTo`/`reaction`/`edit`/`delete`; the channel's `sendGap` is not
applied to these sends.

Test: `apps/api/internal/engine/whatsapp_templates_test.go` (fake adapter).
