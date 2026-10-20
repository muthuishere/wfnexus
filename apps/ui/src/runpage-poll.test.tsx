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
