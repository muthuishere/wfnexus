---
name: dead-code-finder
description: "Inventory code that looks unused — unreachable functions, unused exports, unused dependencies, unused files — with tool output as evidence for every candidate. Finds; never deletes. Trigger on: find dead code, what is unused, cleanup candidates, inventory unused code."
---
# Find dead-code candidates

You produce a LIST, not a verdict. A candidate is something a tool says nothing
reaches. Whether it is really safe to remove is the next step's job
(`usage-prover`), and it is often not: a finder cannot see a template, a doc, a
skill, reflection, or a caller that was lost by mistake.

**Every candidate carries the tool output that produced it.** A candidate you
thought of yourself, with no tool behind it, is not a candidate — leave it out.

## The finders, per language

Run what applies. Prefer the installed binary; fall back to the pinned `go run`
/ `npx` form so this works on a machine that has installed nothing.

| what | command |
|---|---|
| Go: unreachable funcs (whole program, tests included) | `deadcode -test ./...` or `go run golang.org/x/tools/cmd/deadcode@latest -test ./...` |
| Go: unused identifiers, fields, consts | `staticcheck -checks U1000 ./...` or `go run honnef.co/go/tools/cmd/staticcheck@latest -checks U1000 ./...` |
| Go: unused module requirements | `go mod tidy -diff` (prints what tidy would remove; changes nothing) |
| TS/JS: unused files, exports, deps, types | `npx --yes knip@5 --reporter json` (in the package dir) |
| Callers of one symbol (any language the repo's graph covers) | `ctx-optimize card <symbol>` / `ctx-optimize affected <symbol>` when `.ctxoptimize/` exists |

Run Go finders from each module root (`go.mod`); run knip from each package with
a `package.json`. A monorepo has several of each.

## Output

Write `candidates.jsonl`, one line per candidate, in exactly this shape — it is
what `usage-prover` and `wfx judge` read:

```json
{"id": "engine.applyDecideGates", "kind": "unreachable-func", "file": "apps/api/internal/engine/engine.go", "line": 854, "symbol": "Engine.applyDecideGates", "tool": "deadcode -test", "evidence": "internal/engine/engine.go:854:18: unreachable func: Engine.applyDecideGates"}
```

`kind` is one of: `unreachable-func`, `unused-ident`, `unused-export`,
`unused-file`, `unused-dep`, `unused-type`.

Then report: how many candidates per kind and per tool, and which finders you
could NOT run and why (a missing toolchain is a gap in the inventory, and it
has to be said, not skipped).

## Rules

- Do not edit anything. This step has no reason to write outside its output.
- Keep the whole list, however long. Filtering is the next step's job; a
  candidate you silently dropped can never be proven either way.
- Test-only helpers flagged in `_test.go` files are candidates too, marked
  `"testOnly": true` — dead test helpers are real clutter, and they are also the
  lowest-risk removals.
- Generated code (a `// Code generated ... DO NOT EDIT.` header, `*.pb.go`,
  `dist/`, lockfiles) is never a candidate. Say how many you excluded this way.
