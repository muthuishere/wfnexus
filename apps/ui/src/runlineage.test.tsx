import { test, expect, afterEach } from 'vitest'
import { render, cleanup } from '@testing-library/react'
import type { Run } from './api'
import { ChildRuns, ParentLine } from './components/RunLineage'

afterEach(() => cleanup())

const base: Run = {
  id: '11111111-2222-3333-4444-555555555555', project: 'local', workflow: 'pingpong', status: 'done',
  input: {}, currentStep: '', error: '', createdAt: '2026-11-06T00:00:00Z', updatedAt: '2026-11-06T00:00:00Z',
}

// ADR 0026: a child run links back to the run whose `start:` step started it.
test('a child run names its parent and the step that started it', () => {
  const { getByTestId } = render(<ParentLine run={{ ...base, parentRunId: 'aaaaaaaa-0000-0000-0000-000000000000', parentStepId: 'reply', parentMode: 'wait' }} />)
  const line = getByTestId('run-parent')
  expect(line.textContent).toContain('aaaaaaaa')
  expect(line.textContent).toContain('reply')
  expect(line.textContent).toContain('waits for it')
  expect(line.querySelector('a')?.getAttribute('href')).toBe('#/runs/aaaaaaaa-0000-0000-0000-000000000000')
})

test('a top-level run says nothing about a parent', () => {
  const { container } = render(<ParentLine run={base} />)
  expect(container.textContent).toBe('')
})

test('the runs a run started are listed with their status', () => {
  const kids: Run[] = [
    { ...base, id: 'c1c1c1c1-0000-0000-0000-000000000000', workflow: 'whatsapp-sender', status: 'done', parentStepId: 'send' },
    { ...base, id: 'c2c2c2c2-0000-0000-0000-000000000000', workflow: 'digest', status: 'running', parentStepId: 'send' },
  ]
  const { getByTestId } = render(<ChildRuns runs={kids} />)
  const card = getByTestId('run-children')
  expect(card.querySelectorAll('a')).toHaveLength(2)
  expect(card.textContent).toContain('whatsapp-sender')
  expect(card.textContent).toContain('running')
  const { container } = render(<ChildRuns runs={[]} />)
  expect(container.textContent).toBe('')
})
