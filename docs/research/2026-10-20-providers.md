# wfnexus providers: state, doctor port clash, routing design (2026-10-20)

Answers for the CEO (foreman lean plan) and airouter. This was research and tests only: no installs, and no
config changes on prod.

## Summary

- **Two paths work on the prod server today:** `claude -p` (`claude-headless`) and opencode free models in
  `--standalone` mode. OpenRouter's key works from the Mac but isn't set on the server.
- **The doctor failure has two causes.** It's a port shared between Linux users, and opencode's Go login is
  missing on the server.
- **Recommendation:** keep routing inside wfnexus, and don't make routsi a required hop.

## 1. What passes a real test today

`wfx doctor` runs against the **prod-db server** (wfnexus user, linux/amd64) through the `deemwar` context.

| provider | where | test | result |
|---|---|---|---|
| claude -p (`claude-headless`, sonnet) | prod-db | `claude -p --model sonnet "reply with exactly: ok"` | **PASS** `ok` |
| opencode CLI, default `opencode-go/kimi-k2.7-code` | prod-db | `opencode run -m opencode-go/kimi-k2.7-code "…"` | **FAIL**: service port in use (see 2) |
| opencode CLI `--standalone`, Go model | prod-db | `opencode run --standalone -m opencode-go/kimi-k2.7-code "…"` | **FAIL** `Model unavailable`: no Go login on the server (0 `opencode-go` models listed) |
| opencode CLI `--standalone`, free model | prod-db | `opencode run --standalone -m opencode/deepseek-v4.1-flash-free "…"` | **PASS** `ok` |
| opencode ACP (preset `opencode` → `opencode acp`) | prod-db | not run separately; it uses the same binary and login | expected to hit the same two failures until they're fixed |
| OpenRouter (HTTP, `gpt-oss`) | Mac | `sec exec -- curl …/chat/completions` with `{{OPENROUTER_API_KEY}}`, `openai/gpt-oss-120b` | **PASS** `ok`, cost $0.00001 |
| OpenRouter | prod-db | `wfx doctor` | **FAIL** `OPENROUTER_API_KEY is not set` on the server |
| jev-direct (TypeSafe) | prod-db | `wfx doctor` | **PASS** (key present) |
| classifier `jev` | prod-db | `wfx doctor` | **FAIL**: it is wired to OpenRouter, whose key is unset |

A gotcha seen in testing: on a reasoning model, `max_tokens: 20` spent the whole budget on reasoning and
returned `content: null`. Give judges and classifiers at least 300.

## 2. The doctor port clash: cause and fix

**Cause.** opencode v2 runs a background "managed service" on a fixed port, `127.0.0.1:49374`. That port is
the same for **every Linux user on the machine**. prod-db also has a user `agent`, which already runs
`/home/agent/.opencode/bin/opencode serve --service` and holds that port. So every opencode call by the wfnexus
user fails with `Managed service port 49374 … already in use`.

**Fix.** Pick one; each is a small change for the wfnexus user only:

1. `opencode service set port 49375` as wfnexus (one port per user). This is the simplest.
2. Have wfnexus's opencode presets pass `--standalone` (on `run`, and check `acp`). There's then no
   shared service at all, and every step gets its own process, which matches `SessionPerTurn` (a fresh
   session per turn). Better for isolation. It's a code change to the presets in
   `apps/api/internal/devinadapter/acp.go:135` and the cli preset.

**Then, separately, log in again:** the Go subscription is missing on the server (`~/.local/share/opencode`
has no auth file under v2.0.32). The owner runs `opencode auth login` as wfnexus and chooses opencode-go. For
API-key providers (OpenRouter, DeepSeek), wfnexus should pass the key by env name from `sec` instead of
relying on opencode's own store.

Doctor should also say which of the two failed. Right now it shows only the raw opencode stack.

## 3. Design fork: routing inside wfnexus, or one external router (routsi)?

**What wfnexus already has.** A provider catalog with three kinds: `http`, `cli` and `acp`
(`internal/catalog/catalog.go:32`, validation at `:319-359`).
- Dispatch is in `internal/engine/provider.go:125` (http) and `:165` (cli/acp).
- The doctor probes are in `internal/engine/doctor.go:271/281`.
- Prices are in `internal/catalog/prices.go`, for cost per run.
- Keys are always an env **name** (`APIKeyEnv`, `catalog.go:57`), never the value.
- The ACP presets (devin, opencode, codex) are in `internal/devinadapter/acp.go:127`.
- The judge has its own provider use in `internal/judge/judge.go`.

**Recommendation: keep execution routing inside wfnexus, and treat any router as just another provider.**

- **CLI and ACP agents can't sit behind a network router.** `claude -p` and `opencode acp` are agent
  harnesses. They need the step's working directory, mounted files, skills, the permission mode and a session
  per turn, on the machine where the step runs. A router service would have to run them on its own box,
  without the workflow's files. That breaks the main thing wfnexus steps do.
- wfnexus needs cost and outcome per step and per run for the run page. With routing inside, it records them
  directly. With an external router, it gets only what the router reports back.
- One fewer hop and one fewer service to keep up during prod incidents. The guardian shouldn't depend on a
  router being up.
- **Where a router does help: plain HTTP model calls.** Those are fallbacks, cost caps and one place for
  keys. OpenRouter already *is* such a router for HTTP models, and LiteLLM or Portkey are the self-hosted
  ones. If routsi exists, it plugs in as an `http` provider with an OpenAI-compatible `baseURL`; wfnexus
  needs no change.

**Trade-off.** Keys and fallbacks get configured in two places: the wfnexus catalog and routsi or OpenRouter.
To limit that, wfnexus keeps a single `default` plus a short ordered fallback list per step, and nothing more
elaborate.

**What to add in wfnexus** (small): a provider-level `fallback: [names]` that is tried on transport or auth
failure, and `--standalone` for opencode.

## 4. Test plan for each provider

Each one gets the same 3 checks, run through wfnexus itself (not only the raw CLI), on prod-db as wfnexus:

1. **Liveness.** `wfx doctor` shows ✓ and its default call is PASS.
2. **Real step.** A one-step workflow ("reply with exactly: ok"), checked with `wfx run --force` and the
   run's outcome. Then a step that reads a mounted file and writes `WFX_STEP_SUMMARY`. This proves the
   cwd, files and skills reach the agent.
3. **Failure path.** Unset the key or point at a bad model, and expect a clean `failed: <reason>` on the run
   page within the timeout, never a hang. With fallbacks configured, the run should land on the next
   provider.

| provider | extra check |
|---|---|
| claude -p | `--permission-mode` honoured; cost shown as "not tracked" or the subscription |
| opencode ACP | two steps in a row: a fresh session each, no growing history; Go model after the login; concurrency 2 (no port clash with `--standalone`) |
| opencode CLI | free-model fallback when the Go quota is out |
| OpenRouter http | cost recorded matches `usage.cost`; reasoning model with `max_tokens` at least 300 |
| jev-direct / classifier jev | classifier rewired to TypeSafe so it doesn't need OpenRouter |

## Incident noted during this research

While checking the port clash, `~/.config/opencode/service.json` **on the Mac** was printed to my session.
It holds the local opencode service password. It's localhost-only, but it should be rotated:
`opencode service stop`, delete the file, then `opencode service start` makes a new one.

## Update: the owner's decision (later on 2026-10-20)

routsi (`~/muthu/gitworkspace/llm-forward-proxy`, github muthuishere/routsi) is the one place for providers,
keys and aliases. It will **also host the agent harnesses**: Claude over ACP, and opencode/OpenRouter over ACP.
wfnexus does not build a router. It calls routsi for model calls and for agent steps, and keeps `claude -p`
embedded only as a fallback. The open requirement for routsi is that agent steps run in the step's working
directory (checkout, mounted files, skills), with a fresh session per turn, cost reported back, and a
timeout or cancel.
