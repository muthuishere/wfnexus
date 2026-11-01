// Loops on a run page. The engine persists every loop iteration as its own
// step row — `review[2]` for a step that repeats itself, `fix-loop[1].check`
// for a body step (apps/api/internal/workflow/loop.go). The definition only
// knows `review` and `fix-loop`, so the run page expands each loop step into
// its iteration rows, giving each the definition it is an iteration OF — its
// approvers, its schema, its kind — so the timeline, the step panel and the
// "waiting on" line all work for an iteration exactly as for a step.
import type { Step, StepRun } from '../api'

/** `fix-loop[1].check` → { parent: 'fix-loop', index: 1, body: 'check' } */
export function splitIterationId(id: string): { parent: string; index: number; body: string } | null {
  const m = /^(.*?)\[(\d+)\](?:\.(.+))?$/.exec(id)
  if (!m) return null
  return { parent: m[1], index: Number(m[2]), body: m[3] || '' }
}

export const isLoop = (s: Step) => s.foreach !== undefined || !!s.until?.length || !!s.while?.length

/** The definition list with each loop step followed by its iteration rows. */
export function withIterations(defs: Step[], rows: StepRun[]): Step[] {
  const out: Step[] = []
  for (const def of defs) {
    out.push(def)
    if (!isLoop(def)) continue
    const mine = rows
      .filter(r => splitIterationId(r.stepId)?.parent === def.id)
      .sort((a, b) => a.position - b.position)
    for (const r of mine) {
      const it = splitIterationId(r.stepId)!
      const base = it.body ? def.steps?.find(b => b.id === it.body) : def
      if (!base) continue
      out.push({
        ...base, id: r.stepId, name: `↳ ${r.stepId}`,
        foreach: undefined, until: undefined, while: undefined, steps: undefined,
      })
    }
  }
  return out
}
