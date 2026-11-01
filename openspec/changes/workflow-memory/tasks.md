## 1. Model and storage

- [x] 1.1 `workflow.Memory` (bool or object) on `Definition` and `Step`, both dialects, validation of
      include entries, budget and retain.
- [x] 1.2 `workflow_memory` table: Postgres `000021`, SQLite `000015`; store CRUD, newest-first order.

## 2. Engine

- [x] 2.1 Record a run at every terminal status (scrubbed, truncated, never failing the run).
- [x] 2.2 `remember` tool for agent steps with memory on.
- [x] 2.3 Load own + included memory into `TemplateData.Memory`; append to agent prompts.
- [x] 2.4 Consolidation: retain cap, rolling capped digest, lesson cap.
- [x] 2.5 Dry run reports the memory that would be loaded.

## 3. Surfaces

- [x] 3.1 REST: `GET/DELETE /api/workflows/{name}/memory`, `DELETE /api/memory/{id}`.
- [x] 3.2 CLI: `wfx memory ls|show|forget`.
- [x] 3.3 UI: Memory section on the workflow page.
- [x] 3.4 Docs: concepts/state-and-memory.mdx, ROADMAP.

## 4. Verification

- [x] 4.1 Engine tests: run 2 sees run 1, opt-out, include another workflow, budget truncation,
      redaction; store tests on SQLite and Postgres (pgtest).

## 5. Deferred

- [ ] 5.1 Optional distilled "lessons" summary via the configured classifier/cheap model.
- [ ] 5.2 Remote (worker) steps calling `remember` through the run's API door.
