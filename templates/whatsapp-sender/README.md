# whatsapp-sender

The one way a workflow speaks on WhatsApp. Every other default workflow sends
through it with `start: {workflow: whatsapp-sender, wait: true}`.

**Input** (all strings): `chat` (required), `text`, `replyTo` + `replyToSender`,
`mentions`, `media` + `mediaKind` (file|image|voice|sticker), `reaction` +
`reactTo`, `edit`, `delete`, `responsePolicy` (draft|approval|auto|none,
default auto), `key` (idempotency; default = hash of the request).

**Policy:** `auto` sends · `approval` pauses at step `approve` with the op
shown · `draft` returns the draft, sends nothing · `none` skips.

**Send:** one `send:` step on the server's default channel (the channel
contract, docs/channels.md). Every op — text, reply, attachment, reaction,
edit, delete — is paced by the channel's `sendGap`, recorded by the adapter's
echo ledger, and claimed once by `key` before the adapter runs.

**Output** (`deliver` stdout, one JSON object):
`{status: sent|drafted|skipped|already, id, action, chat, key}`. A refused send
fails the run (the key is released, so a retry may send); an unconfirmed one
fails it and keeps the key, so it is never resent (`already`).

**Fill:** the default channel must be the WhatsApp channel. An attachment path
must be inside the run's workspace.

Test: `apps/api/internal/engine/whatsapp_templates_test.go` (fake adapter).
