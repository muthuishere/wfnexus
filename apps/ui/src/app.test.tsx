import { describe, test, expect, beforeAll, afterEach } from 'vitest'
import { render, screen, cleanup, waitFor, within } from '@testing-library/react'
import App from './App'
import { api } from './api'

// These mount the real components and let them make real fetches to a real
// server. A React render that throws takes the whole tree down, so each test
// also fails on a captured error — the run page shipped exactly that bug (a
// step with no `skills:` serialised as null and `.map` threw) and the Go tests
// could not see it, because the API answered 200 with correct JSON.
const thrown: unknown[] = []
beforeAll(() => {
  window.addEventListener('error', e => thrown.push(e.error ?? e.message))
  window.addEventListener('unhandledrejection', e => thrown.push(e.reason))
})
afterEach(() => { cleanup(); expect(thrown).toEqual([]) })

function go(hash: string) {
  window.location.hash = hash
  return render(<App />)
}

describe('the UI a person actually downloads', () => {
  test('names the backend it is really running on', async () => {
    go('#/projects')
    // Hardcoded "postgres · s3" here was a lie the moment sqlite existed.
    await screen.findByText(/toolnexus · sqlite · folder/)
  })

  test('lists the workflows on disk', async () => {
    go('#/workflows')
    await screen.findByText('deterministic')
    await screen.findByText('failing')
  })

  test('a run reaches done and its page renders a step with no skills or tools', async () => {
    // Start it through the same API call the "Start run" button makes.
    const run = await api.createRun('deterministic', { label: 'jsdom' })
    await waitFor(async () => {
      const d = await api.run(run.id)
      expect(d.run.status).toBe('done')
    }, { timeout: 20_000, interval: 250 })

    go(`#/runs/${run.id}`)
    // This is the assertion that would have caught the white screen: the page
    // paints, and the two fields that used to arrive as null are on it.
    const steps = await screen.findByText('1. greet')
    expect(steps).toBeTruthy()
    await screen.findByText('2. count')
    const kv = document.querySelector('.kv')!
    expect(within(kv as HTMLElement).getByText('skills')).toBeTruthy()
    expect(within(kv as HTMLElement).getByText('tools')).toBeTruthy()
  })

  test('a failing step is shown as failed, and says why', async () => {
    const run = await api.createRun('failing', {})
    await waitFor(async () => {
      const d = await api.run(run.id)
      expect(d.run.status).toBe('failed')
    }, { timeout: 20_000, interval: 250 })

    go(`#/runs/${run.id}`)
    await waitFor(() => expect(document.querySelector('.badge.failed')).toBeTruthy())
    await screen.findAllByText(/the command exited with 3/)
  })

  test('every page renders without throwing', async () => {
    for (const route of ['#/projects', '#/runs', '#/workflows', '#/templates', '#/workflows/new', '#/skills', '#/workers', '#/system']) {
      const { container, unmount } = go(route)
      // An app that threw renders nothing; this is the cheap general detector
      // for the whole class of bug.
      await waitFor(() => expect(container.querySelector('.top')).toBeTruthy())
      unmount()
    }
  })
})
