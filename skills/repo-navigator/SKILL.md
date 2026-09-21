---
name: repo-navigator
description: Fast orientation inside an unfamiliar repository — build/test commands, layout, and locating the code behind a symptom.
---
# Navigate a repo

- Entry points: README, Makefile / Taskfile.yml / package.json scripts / go.mod / pyproject.toml / Cargo.toml. Note the test command before anything else.
- Locate by symptom: grep the exact error text, the endpoint path, the UI label, the config key. Follow callers with grep, not guesses.
- Prefer `grep`/`glob` over `bash find|cat`; read whole files only when small.
- If `.ctxoptimize/` exists at the root and `ctx-optimize` is on PATH, run `ctx-optimize query "<terms>"` first — it is a prebuilt code graph.
