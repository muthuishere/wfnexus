import { test, expect, afterEach, vi } from 'vitest'
import { render, cleanup, act } from '@testing-library/react'
import { api, type RunDetail } from './api'
import RunPage from './pages/RunPage'

afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.useRealTimers() })

// Over the wfx SSH tunnel a run detail (~200KB) takes over a second, and the
// page used to re-fetch it every second while the run was live — requests
// stacked up behind each other. SSE drives refreshes now; the poll is a 10s
// safety net, and never more than one fetch is in flight.
test('a live run page does not poll the detail every second, nor overlap fetches', async () => {
  vi.useFakeTimers()
  const detail = {
    run: { id: 'r1', workflow: 'w', status: 'running', input: {}, createdAt: new Date().toISOString() },
    steps: [], artifacts: [], definition: null,
  } as unknown as RunDetail
  let inflight = 0, maxInflight = 0
  const run = vi.spyOn(api, 'run').mockImplementation(() => {
    inflight++; maxInflight = Math.max(maxInflight, inflight)
    // slower than the old 1s poll, like the tunnel
    return new Promise(res => setTimeout(() => { inflight--; res(detail) }, 1500))
  })
  vi.spyOn(api, 'events').mockImplementation(() => () => {})
  vi.spyOn(api, 'workflowSchedule').mockResolvedValue({ nextRunAt: null, crons: [] })

  render(<RunPage id="r1" />)
  for (let i = 0; i < 9; i++) await act(async () => { await vi.advanceTimersByTimeAsync(1000) })

  expect(maxInflight).toBe(1)
  // the first load only: 9s with the old 1s poll would have been ~9 calls
  expect(run.mock.calls.length).toBeLessThanOrEqual(2)
})

// Run A -> run B while A's detail is still in flight: A's answer must never be
// painted on B's page (it used to stay there for good — App reused the
// instance, and the shared in-flight flag swallowed B's first fetch).
test('navigating from one run to another never shows the first run', async () => {
  const { default: App } = await import('./App')
  vi.useFakeTimers()
  const mk = (id: string, name: string) => ({
    run: { id, workflow: name, project: 'p', status: 'done', input: {}, createdAt: new Date().toISOString(), updatedAt: new Date().toISOString() },
    steps: [], artifacts: [], definition: null,
  } as unknown as RunDetail)
  vi.spyOn(api, 'run').mockImplementation(id => new Promise(res =>
    setTimeout(() => res(id === 'A' ? mk('A', 'wf-aaa') : mk('B', 'wf-bbb')), id === 'A' ? 3000 : 100)))
  vi.spyOn(api, 'events').mockImplementation(() => () => {})
  vi.spyOn(api, 'workflowSchedule').mockResolvedValue({ nextRunAt: null, crons: [] })
  location.hash = '#/runs/A'
  const { container } = render(<App />)
  await act(async () => { await vi.advanceTimersByTimeAsync(500) }) // A still in flight
  location.hash = '#/runs/B'
  await act(async () => { window.dispatchEvent(new HashChangeEvent('hashchange')) })
  for (let i = 0; i < 5; i++) await act(async () => { await vi.advanceTimersByTimeAsync(1000) })
  expect(container.textContent).toContain('wf-bbb')
  expect(container.textContent).not.toContain('wf-aaa')
  location.hash = ''
})

// main.tsx renders in StrictMode, which mounts, unmounts and mounts again. The
// "drop answers for a run this page no longer shows" guard was cleared by the
// first unmount and never set again: every run page hung on "loading…".
test('a run page under StrictMode loads', async () => {
  const { StrictMode } = await import('react')
  vi.spyOn(api, 'run').mockResolvedValue({
    run: { id: 'S', workflow: 'wf-strict', project: 'p', status: 'done', input: {}, createdAt: new Date().toISOString(), updatedAt: new Date().toISOString() },
    steps: [], artifacts: [], definition: null,
  } as unknown as RunDetail)
  vi.spyOn(api, 'events').mockImplementation(() => () => {})
  vi.spyOn(api, 'workflowSchedule').mockResolvedValue({ nextRunAt: null, crons: [] })
  const { findAllByText } = render(<StrictMode><RunPage id="S" /></StrictMode>)
  expect((await findAllByText(/wf-strict/, undefined, { timeout: 2000 })).length).toBeGreaterThan(0)
})
