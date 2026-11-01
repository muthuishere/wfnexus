## Context

Steps are toolnexus agents assembled per step (`engine/agent.go`). State already offers four explicit
namespaces. toolnexus' `agents.MemoryTool`/`FromDir` is a file-backed agent-home memory; wfnexus runs
are server-side, multi-tenant and must work on Postgres and SQLite, so the record lives in the run
database next to the runs it describes. The toolnexus helper is not reused (it would put memory on the
filesystem of whichever machine ran the step).

## Decisions

1. **Deterministic record first.** The run record is built from rows the engine already has (run input,
   step rows, resolutions, outcome). No model call is needed for memory to work. An LLM-distilled
   "lessons" summary is deferred (see tasks) — when added it must be optional and non-blocking.
2. **Written inside `setRun`** for the terminal statuses, before the status row is updated, so a reader
   that sees a run `done` can already see its memory. A `cancelled` that arrives after a record exists
   (e.g. a cancel on a finished run) does not overwrite it; a retried run's later terminal status does.
3. **Key = (project, workflow).** Own memory is this run's project + workflow name. `workflow:<name>`
   resolves in the same project; `workflow:<project>/<name>` is the only way across projects; `project`
   is every other workflow in this project; `global` is everything else.
4. **Bounded by characters, budgeted in tokens.** `budget` is tokens (default 1500), converted at 4
   chars/token. Order of filling: lessons, recent runs (newest first, at most 5 shown), digest, then
   included sources. Anything past the budget is cut with an explicit marker.
5. **Appended to the prompt, not the soul.** The step's stored prompt then shows exactly what the agent
   read, and the same text reaches a worker running the step remotely. A prompt that references
   `.Memory` positions it itself.
6. **The `remember` tool is granted by default** to agent steps with memory on. It writes only into this
   run's own workflow memory (address taken from the run row, never from the model), so it does not
   widen reach. `memory: false` removes it. Not offered on a worker engine (no store).
7. **Scrubbing** reuses `redactText` with the run's registered secrets plus every current env-store value
   of the run's project, so a record written after a restart is still scrubbed.

## Risks

- Prompt growth → hard budget, modest default, `memory: false` per step.
- Stale or wrong lessons steering later runs → the section is framed as context, not instructions;
  `wfx memory forget` and the UI delete individual entries.
