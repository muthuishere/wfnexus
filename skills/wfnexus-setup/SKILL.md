---
name: wfnexus-setup
description: Take a machine from nothing to a working wfnexus install on Docker, on free models, ending in READY or a named blocker. Trigger: install wfnexus, set up wfnexus, get wfnexus running, is my wfnexus ready.
---
# wfnexus setup

One job: a person says "set up wfnexus" and, a few minutes later, has a server they
can open in a browser, a `wfx` CLI logged in to it, the agent skills installed, and a
model that answers, with no money spent. You finish with exactly one of:

- `READY http://localhost:<port>`, or
- `BLOCKED: <stage>` plus the one thing the person has to do.

Never end on "should be working". The script checks every stage; report what it said.

## Run it

From a wfnexus checkout (clone `https://github.com/muthuishere/wfnexus` first if
there is none):

```sh
bash skills/wfnexus-setup/scripts/setup.sh                 # port 8090, project wfnexus
bash skills/wfnexus-setup/scripts/setup.sh --port 8190     # when 8090 is taken
bash skills/wfnexus-setup/scripts/setup.sh --project wfx2 --port 8290   # a second stack beside the first
```

It is idempotent: run it again after fixing a blocker and it picks up where it
stopped. Stages, in order (`references/stages.md` has the detail and every blocker):

| # | stage | what it does |
|---|---|---|
| 1 | docker | `docker info`, `docker compose version` |
| 2 | checkout | finds the repo, copies `compose.env.example` to `.env` if there is none |
| 3 | up | `docker compose up -d --build` (refuses a port someone else holds) |
| 4 | health | polls `/api/health`, up to 5 minutes. The first boot exits once on purpose; the restart policy brings it back |
| 5 | cli | `wfx` must be on PATH (`sh install.sh` or `task install` if not) |
| 6 | skills | `wfx install --skills` into `~/.claude/skills` and `~/.agents/skills` |
| 7 | login | device login, approved with the bootstrap admin token from the server log |
| 7b | step credential | a second login stored as `WFX_API_TOKEN`, so `wfx judge` inside a step is allowed |
| 8 | keys | `TYPESAFE_API_KEY`, `OPENROUTER_API_KEY` from your environment into `wfx env set` |
| 9 | models | `wfx models opencode-acp --free` |
| 10 | doctor | `wfx doctor`; READY needs the default provider ready |

## Secrets: the rules you follow here

- **Never print a token or a key.** The script reads the admin token from the log into
  a variable and hands it to curl on stdin. Do not "help" by grepping the log yourself.
  If you must see whether a token exists, grep for the label, not the value:
  `docker compose logs wfx-server | grep -c 'admin token'`.
- **Keys go in by pipe, never argv:** `printf '%s\n' "$TYPESAFE_API_KEY" | wfx env set TYPESAFE_API_KEY`.
  If the machine has `sec`, prefer `sec run TYPESAFE_API_KEY -- sh -c 'printf "%s\n" "$TYPESAFE_API_KEY" | wfx env set TYPESAFE_API_KEY'`.
  If the key is not in the environment, tell the person to run `wfx env set TYPESAFE_API_KEY`
  themselves: it prompts with echo off. Do not ask them to paste it into the chat.
- Nothing goes into `.env` that you did not see the person choose. A key in `.env`
  reaches every step's environment; the env store only reaches the steps that name it.

## Free models

The default provider is `opencode-acp`: opencode running **inside the container**, on
opencode Zen's free models. They need no key and no login. `jev-direct`, the default
classifier, is the one piece that needs a key (`TYPESAFE_API_KEY`); without it agent
steps still run, and `decide:` / `wfx judge` are refused with the key's name.

`wfx models opencode-acp --free` lists what opencode offers **today**; the free list
rotates. If it says the configured model is "NOT offered", do not panic: in rehearsal
the configured `opencode/space-bunny-free` answered anyway. If a step does refuse to
start, give that step `model: opencode/<a free id from the list>`.

A paid model instead: `docker compose exec wfx-server opencode auth login`.

## Prove it (do this before saying READY to a person who will demo it)

```sh
wfx templates                        # the shipped templates are there
wfx run <a workflow> -f              # a real run, streamed
```

`references/stages.md` ends with a two-step smoke workflow (an agent step on the free
model, then `wfx judge` from inside a step) that exercises the whole path in about 10s.

## What this skill does not do

- Deploy a workflow to a team server: that is `wfnexus-deploy`.
- Write a workflow: that is `workflow-author` / `workflow-recipes`.
- Touch a server it did not start. If `localhost:8090` is already a wfnexus someone
  runs (a launchd server, say), use `--port` and `--project` for a separate stack.
