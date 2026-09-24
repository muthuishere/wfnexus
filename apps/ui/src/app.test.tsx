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

  // A LISTING SHOWS THE WHOLE WORKFLOW.
  //
  // `carrier` is a directory-form workflow whose step runs a script beside it.
  // A list that showed only its name would look reusable and not be: you copy
  // it, and the first run fails on a file nobody mentioned. So the project's
  // table names every file — including the README, which the platform has no
  // idea about and carries anyway.
  test("a project's workflows list the files that travel with them", async () => {
    go('#/projects/local')
    await screen.findByText('carrier')
    await screen.findByText('greet.sh')
    await screen.findByText('lib/phrase.sh')
    await screen.findByText('README.md')
    // With sizes, so "is this the whole thing" is answerable from the list.
    expect(screen.getAllByText(/\d+ B$/).length).toBeGreaterThan(0)
    // And a flat one-file workflow says so rather than showing nothing.
    await screen.findAllByText('just the YAML')
  })

  // REUSE CARRIES EVERYTHING, and the proof is a run, not a byte count: the
  // copy prints a phrase that exists only inside the script beside it, and
  // only if that script's own `lib/phrase.sh` came along too.
  test('copying a workflow brings its files, and the copy runs', async () => {
    const as = `carried-${Date.now().toString(36)}`
    await api.copyWorkflow('carrier', as)
    const copy = await api.workflow(as)
    expect(copy.files?.map(f => f.path).sort()).toEqual(['README.md', 'greet.sh', 'lib/phrase.sh'])

    const run = await api.createRun(as, {})
    await waitFor(async () => {
      expect((await api.run(run.id)).run.status).toBe('done')
    }, { timeout: 20_000, interval: 250 })
    const d = await api.run(run.id)
    expect(JSON.stringify(d.steps?.[0]?.output)).toContain('the sidecar travelled')
  })

  test("a workflow's state is visible on its page", async () => {
    // What a person needs when a scheduled workflow says it is up to date: the
    // watermark itself, on the page, instead of a database client.
    await api.setState({ scope: 'workflow', name: 'deterministic', key: 'last_id', value: '4120' })
    await api.setState({ scope: 'global', key: 'tier', value: 'pro' })

    go('#/projects/local/deterministic')
    await screen.findByText('last_id')
    await screen.findByText('4120')
    // The global namespace is shared, so it shows here too — labelled, because
    // these are four separate namespaces and which one a value is in matters.
    await screen.findByText('tier')
    expect(screen.getAllByText('workflow').length).toBeGreaterThan(0)
    expect(screen.getAllByText('global').length).toBeGreaterThan(0)
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
