# Setup stages, blockers, and the smoke test

Each stage of `scripts/setup.sh` prints `ok  <stage>` or `BLOCKED <stage>: <why> -> <do this>`.
This is every blocker it can name, and what fixes it.

| stage | blocker | fix |
|---|---|---|
| docker | docker is not installed | Install Docker Desktop, OrbStack or colima |
| docker | the Docker daemon is not running | Start it; `docker info` must answer |
| checkout | no wfnexus checkout found | `git clone https://github.com/muthuishere/wfnexus`, then `--dir wfnexus` |
| port | port N is in use by something else | `--port <free port>`. Check with `lsof -nP -iTCP:N -sTCP:LISTEN` |
| compose | docker compose up failed | Read the output. The first build compiles Go and the UI and installs opencode (~3-5 min); a network failure during `npm install` is the usual one |
| health | /api/health did not answer | `docker compose -p <project> logs wfx-server`. A refused boot with "no users" is normal ONCE; twice means the restart policy is off |
| cli | wfx is not on PATH | `sh install.sh` (release binary, checksum-verified) or `task install` from the checkout |
| skills | install failed | `wfx install --skills` must run from the checkout (it finds `skills/` there), or set `WFX_SKILLS_DIR` |
| login | no bootstrap admin token in the log | The token is printed once, on the first boot of an EMPTY database. If the volume already has users, a person who is an admin runs `wfx login --url <url>` and approves the code in the browser. Starting over: `docker compose -p <project> down -v` (deletes everything) |
| login | device approval failed | The code expired (15 min) or was burned by 5 wrong tries; re-run the script |
| doctor | the default provider ... is not ready | Usually opencode missing from the image (rebuild) or `WFX_DEFAULT_PROVIDER` naming a provider that needs a key |

Notes, not blockers:
- `skills: 0 installed, N already there`: existing skills are not overwritten. `wfx install --skills --force`
  replaces them with this checkout's versions.
- `not set: TYPESAFE_API_KEY`: agent steps work; decisions and `wfx judge` do not until it is set.
- doctor lists dozens of `✗` providers: every provider in the registry is checked, and most are
  optional ones nobody configured (claude-cli, gemini, ...). Only the default provider decides READY.

## Where things live

| thing | where |
|---|---|
| server | `http://localhost:<port>` (UI and API) |
| your login | `~/.config/wfx/contexts.json` (0600), or `$WFX_CONTEXTS` |
| env store | Postgres, encrypted with `/data/secret.key` on the `data` volume |
| opencode login (paid models only) | the `opencode` volume |
| images | `wfnexus/wfx-server:local` |

## Smoke test: the whole path in one workflow

Save as `smoke.yaml`, then `wfx apply smoke.yaml && wfx run smoke -f`:

```yaml
name: smoke
description: An agent step on the default free model, then wfx judge from inside a step.
on: { workflow_dispatch: }
input_schema: { type: object }
steps:
  - id: status
    tools: []
    budget: { max_turns: 4, max_wall_sec: 300 }
    prompt: Write a one-sentence status saying the stack is up. Call submit_output with it.
    output_schema:
      type: object
      additionalProperties: false
      required: [status]
      properties: { status: { type: string } }
  - id: judge
    needs: [status]
    run: |
      cat > q.yaml <<'Q'
      concrete:
        type: noul
        instructions: Does this status name a concrete result?
      Q
      wfx judge -q q.yaml --classifier jev-direct --state "{{ .Steps.status.status }}"
    output_schema:
      type: object
      properties: { ok: { type: boolean }, exitCode: { type: integer }, stdout: { type: string }, stderr: { type: string } }
```

What a good run shows: the agent line names `opencode-acp/opencode/<free model>`, and the judge
step prints `{"answers":{"concrete":{"noul":0.9,"band":"yes"}}...}` then `judged 1 item(s) on
jev-direct on http://127.0.0.1:8090`. The judge step's `stderr` is where a missing key or a
missing step credential shows up. A failed `run:` step still ends `done` with `ok: false`, so
read the output, not only the status.
