import { describe, expect, it } from 'vitest'
import type { Step, StepRun } from './api'
import { splitIterationId, withIterations } from './lib/loops'
import { validateDraft } from './builder/validate'

const step = (s: Partial<Step>): Step => ({ id: 'x', name: '', skills: [], tools: [], outputSchema: {}, ...s }) as Step
const row = (stepId: string, position: number): StepRun => ({ stepId, position, status: 'done' }) as StepRun

describe('loop iteration rows on a run page', () => {
  it('parses iteration ids, including dotted job ids', () => {
    expect(splitIterationId('job.fix-loop[11].check')).toEqual({ parent: 'job.fix-loop', index: 11, body: 'check' })
    expect(splitIterationId('review[2]')).toEqual({ parent: 'review', index: 2, body: '' })
    expect(splitIterationId('plain')).toBeNull()
  })

  it('puts each loop step’s iterations right after it, carrying the body definition', () => {
    const defs = [
      step({ id: 'list', prompt: 'p' }),
      step({ id: 'fix', until: [{ path: 'check.ok', equals: true }], maxIterations: 3,
        steps: [step({ id: 'patch', prompt: 'p', approvers: ['alice'] }), step({ id: 'check', run: 'true' })] }),
      step({ id: 'after', run: 'true' }),
    ]
    const rows = [row('list', 0), row('fix', 1), row('after', 2),
      row('fix[1].check', 1_100_102), row('fix[0].patch', 1_100_001), row('fix[0].check', 1_100_002), row('fix[1].patch', 1_100_101)]
    const out = withIterations(defs, rows)
    expect(out.map(s => s.id)).toEqual(['list', 'fix', 'fix[0].patch', 'fix[0].check', 'fix[1].patch', 'fix[1].check', 'after'])
    expect(out[2].approvers).toEqual(['alice'])
    expect(out[2].steps).toBeUndefined()
  })
})

describe('loop validation in the builder', () => {
  const draft = (s: Step[]) => ({ name: 'w', description: '', inputSchema: {}, steps: s }) as any
  const msgs = (s: Step[]) => validateDraft(draft(s)).filter(i => i.severity === 'error').map(i => i.message).join('\n')

  it('refuses an until loop without max_iterations', () => {
    expect(msgs([step({ id: 'a', prompt: 'p', outputSchema: { properties: { ok: {} } }, tools: ['bash'], until: [{ path: 'a.ok', equals: true }] })]))
      .toMatch(/needs max_iterations/)
  })

  it('refuses a guard that reads nothing the loop produces', () => {
    expect(msgs([step({ id: 'a', prompt: 'p', outputSchema: { properties: { ok: {} } }, tools: ['bash'], maxIterations: 2, until: [{ path: 'input.x', equals: 1 }] })]))
      .toMatch(/must read something the loop produces/)
  })

  it('accepts a bounded loop over a body without demanding a prompt on the loop step', () => {
    expect(msgs([step({ id: 'a', foreach: 'input.files', steps: [step({ id: 'b', run: 'true' })] })])).toBe('')
  })
})
