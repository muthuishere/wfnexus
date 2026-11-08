# morning-brief

The owner's 08:00 WhatsApp brief, replacing the CEO's by-hand morning check.
`collect` (no AI) gathers failed runs and runs waiting on a person from
`$WFX_API`, open PRs and issues on `$GH_REPO` via `gh`, and the mtime and top
section of the ceo-harness `NOW.md`, plus yesterday's brief; a source it cannot
read becomes a citable `missing:<source>` fact instead of failing the run.
`brief` (one read-only agent turn on the default model) writes at most 12
lines, most urgent first, each citing fact ids; `check` drops any line that
cites nothing or an id not in `facts.json`, and reports what changed since
yesterday. `deliver` hands the text to `whatsapp-sender` with `start: wait:
true` and the input's `responsePolicy` — **default `draft`**, so nothing is
sent until you rerun or schedule it with `responsePolicy: auto`. The schedule
is `30 2 * * *` (cron is UTC: 08:00 IST); set `chat` to your WhatsApp JID.

```sh
wfx run morning-brief --input '{"chat":"<you>@s.whatsapp.net"}'   # draft only
```
