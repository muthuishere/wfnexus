import { test, expect, afterEach } from 'vitest'
import { cleanup, render, fireEvent } from '@testing-library/react'
import type { Run, Step, StepRun } from './api'
import { headlineOf, parseOutcome } from './lib/story'
import Markdown from './components/Markdown'
import RunTimeline from './components/RunTimeline'

afterEach(cleanup)

const run = (o: Partial<Run>): Run => ({
  id: 'r', project: 'local', workflow: 'w', status: 'done', input: {}, currentStep: '', error: '',
  createdAt: '2026-10-16T16:45:00Z', updatedAt: '2026-10-16T16:46:00Z', ...o,
})

test('the outcome line is the headline', () => {
  expect(parseOutcome('incident: api 5xx at 4%')).toEqual({ verdict: 'incident', text: 'api 5xx at 4%' })
  expect(headlineOf(run({ outcome: 'ok: all clear' }))).toEqual({ verdict: 'ok', text: 'all clear' })
  expect(headlineOf(run({ status: 'failed', error: 'command exited 1\nmore' })).text).toBe('command exited 1')
  // a paused run says it is waiting, whatever an earlier step concluded
  expect(headlineOf(run({ status: 'needs_input', outcome: 'ok: fine' })).verdict).toBe('waiting')
})

test('markdown renders as elements, never as injected HTML', () => {
  const { container } = render(<Markdown text={'## Found 2\n- **api** down\n- `db` ok\n\n<img src=x onerror=alert(1)>'} />)
  expect(container.querySelector('.md-h2')?.textContent).toBe('Found 2')
  expect(container.querySelectorAll('li')).toHaveLength(2)
  expect(container.querySelector('b')?.textContent).toBe('api')
  expect(container.querySelector('img')).toBeNull()
})

test('script steps show no model facts; skipped steps fold into one line', () => {
  const steps: Step[] = [
    { id: 'look', name: 'look', run: 'true', skills: [], tools: [], outputSchema: {} },
    { id: 'fix', name: 'fix', skills: [], tools: [], outputSchema: {} },
    { id: 'pr', name: 'pr', skills: [], tools: [], outputSchema: {} },
  ]
  const sr = (stepId: string, status: string): StepRun => ({
    id: stepId, runId: 'r', stepId, position: 0, status, attempts: 1, turns: 0, prompt: '', output: null,
    rawText: '', error: '', usage: null, startedAt: '2026-10-16T16:45:00Z', finishedAt: '2026-10-16T16:45:02Z',
  })
  const byId = { look: sr('look', 'done'), fix: sr('fix', 'skipped'), pr: sr('pr', 'skipped') }
  const { container, getByText } = render(
    <RunTimeline steps={steps} byId={byId} events={[]} now={Date.now()} selected="" onPick={() => {}}
      skipReason="nothing actionable found" />)
  expect(container.textContent).not.toMatch(/turns|tok|not tracked/)
  expect(container.querySelectorAll('.tlrow')).toHaveLength(1)
  fireEvent.click(getByText(/2 steps not needed/))
  expect(container.querySelectorAll('.tlrow')).toHaveLength(3)
})
