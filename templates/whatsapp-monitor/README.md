# whatsapp-monitor

Catches a channel that is connected but not hearing anything. Runs every 5 minutes.

| finding | how |
|---|---|
| `deaf` (critical) | `STORE_PROBE` (read-only) says the WhatsApp store has a message newer than the last one wfnexus saw by more than `LAG_MAX_SEC` |
| `receive-stale` / `receive-error` | `/api/channels`: last successful receive older than `RECEIVE_MAX_SEC`, or the last error |
| `service` | a `whatsapp-group-monitor` service is `silent` or `crashed` (`wfx module ls`) |
| `validating` | stuck validating |

**Output:** `report.json` `{healthy, channel, lastReceive, lastEvent,
storeLatest, lagSec, findings[]}`; `check` stdout is the summary or the alert.

**Alert:** one message to `owner_chat` through `whatsapp-sender`, keyed by the
findings and an `ALERT_EVERY_H` window, so the same problem is sent once per
window. If the check itself breaks, the run fails. It never reports healthy
when it could not check.

**Fill:** the `owner_chat` default, `WA_CHANNEL`, and `STORE_PROBE`: a
read-only command that prints `{"latest": "<iso time>"}`, for example a wrapper
around `apl with whatsapp:biz --read-only messages --limit 1`. Without
`STORE_PROBE`, lag is not measured and the report says so in `note`.
