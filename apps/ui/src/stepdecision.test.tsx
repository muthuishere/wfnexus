import { test, expect, afterEach, vi } from 'vitest'
import { render, cleanup, fireEvent } from '@testing-library/react'
import type { Step, StepRun } from './api'
import { stepDecision, summaryHeadline } from './lib/story'
import RunTimeline from './components/RunTimeline'

afterEach(() => cleanup())

// Step rows below are VERBATIM (trimmed) from run a6e632ae of a 30-step
// workflow on the team server — but nothing here is specific to it: each case
// is one KIND of step, and the rule for it is generic.
const def = (id: string, extra: object = {}) => ({ id, name: id, ...extra } as unknown as Step)
const row = (stepId: string, status: string, extra: object = {}) =>
  ({ stepId, status, ...extra } as unknown as StepRun)

const byId: Record<string, StepRun> = {
  aggregate: row('aggregate', 'done', { output: { exitCode: 3, ok: false,
    outcome: 'incident: 2 finding(s) — top: [warning] AI log check could not run',
    summary: '**incident: 2 finding(s)**' } }),
  'triage-judge': row('triage-judge', 'done', { output: { actionable: 'work_it' }, decision: { calibrated: true, answers: {
    actionable: { choice: 'work_it', confidence: 0.64 }, duplicate: { choice: 'new', confidence: 1 } } } }),
  'work-gate': row('work-gate', 'done', { output: { exitCode: 0, summary: '**work gate:** triage and jev-direct agree: real, ongoing, ours' } }),
  postgres: row('postgres', 'done', { output: { exitCode: 0, summary:
    '### postgres\n| severity | title | first seen | last seen | count |\n|---|---|---|---|---|\n| info | pgmq jobs: 2 delayed msg(s) by design | - | - | 1 |' } }),
  classify: row('classify', 'done', { output: { change_type: 'config', needs_pr: false,
    reason: 'The evidence points to an SSH timeout reaching the observability host, which is a configuration matter' } }),
  prepare: row('prepare', 'done', { output: { exitCode: 0, ok: true, stdout: 'ops: /home/wfnexus/repos/reqsume-ops\nmore', stderr: '' } }),
  'classify-gate': row('classify-gate', 'done', { output: { exitCode: 10 } }),
  'code-rca': row('code-rca', 'skipped'),
  'verify-repro': row('verify-repro', 'skipped'),
  'wait-human': row('wait-human', 'awaiting_approval'),
}

test('each kind of step says what it decided or found, not just "done"', () => {
  const d = (id: string, extra?: object) => stepDecision(def(id, extra), byId[id], byId)
  // a check that wrote an outcome: its verdict and headline
  expect(d('aggregate')).toEqual({ tone: 'incident', text: '2 finding(s) — top: [warning] AI log check could not run' })
  // a judge: each answer, its choice and confidence
  expect(d('triage-judge').text).toBe('actionable: work_it 64% · duplicate: new 100%')
  // a gate script: its summary headline, markdown off
  expect(d('work-gate').text).toBe('work gate: triage and jev-direct agree: real, ongoing, ours')
  // a summary that is a heading + a table: the first row, not "postgres"
  expect(d('postgres').text).toBe('info · pgmq jobs: 2 delayed msg(s) by design')
  // an agent's typed output: its short fields, never the long prose
  expect(d('classify').text).toBe('change_type=config · needs_pr=false')
  // a script with nothing else to say: exit code + its first line
  expect(d('prepare').text).toBe('exit 0 · ops: /home/wfnexus/repos/reqsume-ops')
  // skipped: which `when:` did not hold, and what the value really was
  expect(d('code-rca', { when: [{ path: 'classify-gate.exitCode', equals: 11 }] }))
    .toEqual({ tone: 'skipped', text: 'when classify-gate.exitCode = 11 (was 10)' })
  expect(d('verify-repro', { when: [{ path: 'reproduce.reproduced', equals: true }] }).text)
    .toBe('when reproduce.reproduced = true (reproduce did not run)')
  expect(d('wait-human').tone).toBe('waiting')
})

test('summary headlines skip name-only headings and read tables', () => {
  expect(summaryHeadline('### key-expiry\n_no findings_')).toBe('no findings')
  expect(summaryHeadline('### logs')).toBe('logs')
  expect(summaryHeadline('| a | b |\n|---|---|\n| x | y |\n| z | w |')).toBe('x · y (+1 more)')
  expect(summaryHeadline('**notify via whatsapp:**\n\nWhat: logs check broke')).toBe('notify via whatsapp: What: logs check broke')
})

test('the timeline row shows the decision next to the name; status is an icon with a label', () => {
  const steps = [def('triage-judge'), def('code-rca', { when: [{ path: 'classify-gate.exitCode', equals: 11 }] })]
  const { container } = render(<RunTimeline steps={steps} byId={byId} events={[]} now={Date.now()}
    selected="" onPick={() => {}} />)
  const r = container.querySelector('.tlrow')!
  expect(r.querySelector('.tldecision')!.textContent).toBe('actionable: work_it 64% · duplicate: new 100%')
  expect(r.querySelector('.tldecision')!.getAttribute('title')).toBe('actionable: work_it 64% · duplicate: new 100%')
  expect(r.querySelector('.tlicon')!.getAttribute('aria-label')).toBe('done')
  expect(r.querySelector('.badge.done')).toBeNull() // the word "done" is no longer the row's text
})

// The engine says why it skipped (step_runs.skip_reason); that wins.
test("the engine's own skip reason is shown verbatim", () => {
  const r = row('x', 'skipped', { skipReason: 'not taken by the planner' })
  expect(stepDecision(def('x', { when: [{ path: 'a.b', equals: 1 }] }), r, {}).text).toBe('not taken by the planner')
})

// Runs recorded before the engine stored a reason fall back to replaying the
// guard — by the engine's rules, not new ones (engine/plan.go guardFor).
test('the fallback skip reason follows the engine: input, planner, null, key order', () => {
  const sk = row('s', 'skipped')
  const why = (when: object[], by: Record<string, StepRun>, ctx = {}) =>
    stepDecision(def('s', { when }), sk, by, ctx).text
  // input.* reads the run input, not a step called "input"
  expect(why([{ path: 'input.mode', equals: 'slow' }], {}, { input: { mode: 'fast' } }))
    .toBe('when input.mode = "slow" (was "fast")')
  // a planned run is routed by the planner; a guard replay would contradict it
  expect(why([{ path: 'a.ok', equals: true }], { a: row('a', 'done', { output: { ok: true } }) }, { planned: true }))
    .toBe('not taken by the planner')
  // present-but-null EXISTS (Go: ok=true for a null value)
  const n = { a: row('a', 'done', { output: { v: null } }) }
  expect(why([{ path: 'a.v', exists: false }], n)).toBe('when a.v is absent (was null)')
  // objects compare with sorted keys: {b,a} holds for {a,b}, so the next guard is the one that failed
  const o = { a: row('a', 'done', { output: { o: { b: 2, a: 1 }, n: 3 } }) }
  expect(why([{ path: 'a.o', equals: { a: 1, b: 2 } }, { path: 'a.n', equals: 4 }], o)).toBe('when a.n = 4 (was 3)')
})

// A classifier that could not answer one question leaves a null in answers;
// that used to throw on `a.choice` and blank the whole run page.
test('a null or structured judge answer renders instead of crashing', () => {
  const r = row('j', 'done', { decision: { answers: { first: null, second: { choice: { pick: 'b' }, confidence: 0.5 } } } })
  expect(stepDecision(def('j'), r, {}).text).toBe('first: — · second: {"pick":"b"} 50%')
})

test('a page that throws shows what broke instead of blanking the app', async () => {
  const { default: PageBoundary } = await import('./components/PageBoundary')
  const Boom = () => { throw new Error('answers[0] is null') }
  const quiet = vi.spyOn(console, 'error').mockImplementation(() => {})
  const { getByRole } = render(<PageBoundary><Boom /></PageBoundary>)
  expect(getByRole('alert').textContent).toContain('answers[0] is null')
  quiet.mockRestore()
})

// Planned is the engine's IsPlanned: a goal, or facts. A goal-only workflow with
// guards is routed by the planner, so its skips are "not taken", not a replay.
test('a workflow with a goal reads its skips as not taken by the planner', () => {
  const steps = [def('a'), def('b', { when: [{ path: 'a.ok', equals: true }] })]
  const by = { a: row('a', 'done', { output: { ok: true } }), b: row('b', 'skipped') }
  const { container } = render(<RunTimeline steps={steps} byId={by} events={[]} now={Date.now()}
    selected="" onPick={() => {}} goal="answered" />)
  fireEvent.click(container.querySelector('.tlskipped')!)
  expect([...container.querySelectorAll('.tldecision')].map(e => e.textContent)).toContain('not taken by the planner')
})
