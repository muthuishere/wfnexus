import type { Run, Workflow } from '../api'

// Small facts about workflows and runs that more than one view needs — the
// tree, the project page, the run page — so each one reads them the same way.

/** What may start a workflow. A schedule-only workflow has no Run button, and
 *  saying so is the difference between a dead button and an explanation. */
export function triggerNames(w: Workflow): string[] {
  const on = w.on
  if (!on) return ['workflow_dispatch']
  const out: string[] = []
  if (on.dispatch) out.push('workflow_dispatch')
  if (on.schedule?.length) out.push('schedule')
  if (on.repositoryDispatch) out.push('repository_dispatch')
  if (on.workflowCall) out.push('workflow_call')
  return out.length ? out : ['workflow_dispatch']
}

export function canDispatch(w: Workflow): boolean { return triggerNames(w).includes('workflow_dispatch') }

export function shapeOf(w: Workflow): string {
  if (w.goal) return 'goal-planned'
  if (w.steps?.some(s => s.needs?.length)) return 'parallel'
  return 'sequential'
}

/** A workflow is reachable by its short name and as `project/name`, so a run
 *  recorded under either spelling belongs to the same workflow. */
export function sameWorkflow(runWorkflow: string, name: string): boolean {
  if (runWorkflow === name) return true
  const tail = (s: string) => (s.includes('/') ? s.slice(s.indexOf('/') + 1) : s)
  return tail(runWorkflow) === tail(name)
}

/** The most human field a run's input happens to carry. Input has no fixed
 *  shape, so this is a best guess and the caller falls back to the id — an id
 *  alone makes a list unreadable at a glance, which is the only thing a list is
 *  for. */
export function summariseRun(r: Run): string {
  const i = r.input || {}
  for (const k of ['title', 'report', 'claim', 'question', 'issue_title', 'task', 'target', 'repo_path', 'repo_url']) {
    const v = i[k]
    if (typeof v === 'string' && v.trim()) return v.trim()
  }
  return r.error || ''
}

export function clip(s: string, n: number) { return s.length > n ? s.slice(0, n) + '…' : s }
