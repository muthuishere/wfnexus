import { describe, test, expect, beforeAll, afterEach } from 'vitest'
import { render, screen, cleanup, waitFor, within, fireEvent } from '@testing-library/react'
import App from './App'
import { api, onUnauthenticated } from './api'
import Identity from './components/Identity'

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

  test("a project lists its workflows; the flat lists and the top-level builder are gone", async () => {
    go('#/projects/local')
    const list = await waitFor(() => { const l = document.querySelector('.wflist'); expect(l).toBeTruthy(); return l as HTMLElement })
    await within(list).findByText('deterministic')
    await within(list).findByText('failing')
    const nav = document.querySelector('.side nav') as HTMLElement
    expect(within(nav).queryByText('All runs')).toBeNull()
    expect(within(nav).queryByText('Builder')).toBeNull()
    expect(within(nav).queryByText('Workflows')).toBeNull()
    // Making a workflow starts from the project it goes into.
    expect(screen.getByText('New workflow').closest('a')?.getAttribute('href')).toBe('#/projects/local/+new')
  })

  test('a project can be STARTED from the UI, and a template copied into it', async () => {
    go('#/projects')
    fireEvent.click(await screen.findByText('New project'))
    fireEvent.change(screen.getByPlaceholderText('my-project'), { target: { value: 'started-here' } })
    fireEvent.click(screen.getByText('Create project'))
    // It lands on the new project, empty, with the two ways to begin.
    await waitFor(() => expect(window.location.hash).toBe('#/projects/started-here'))
    cleanup()
    go('#/projects/started-here')
    await screen.findByText('No workflows yet')
    expect(screen.getByText('Start from a template').closest('a')?.getAttribute('href')).toBe('#/projects/started-here/+template')

    // A template copied from inside the project belongs to the project.
    const templates = await api.templates()
    expect(templates.length).toBeGreaterThan(0)
    const r = await api.copyTemplate(templates[0].name, 'first-one', 'started-here')
    expect((r as { path: string }).path).toContain('started-here')
    const p = await api.project('started-here')
    expect(p.workflows).toContain('first-one')
  })

  test('a run waiting on a person shows up on every page, and clears once approved', async () => {
    const run = await api.createRun('gated', { title: 'needs a yes' })
    await waitFor(async () => {
      expect((await api.run(run.id)).run.status).toBe('awaiting_approval')
    }, { timeout: 20_000, interval: 250 })

    // On a page that has nothing to do with it — the indicator lives in the top bar.
    go('#/system')
    fireEvent.click(await screen.findByText(/waiting on you/))
    const item = await screen.findByText('needs a yes', { exact: false })
    expect(item.closest('a')?.getAttribute('href')).toBe(`#/projects/local/gated/runs/${run.id}`)
    await waitFor(() => expect(document.title).toMatch(/^\(\d+\)/))
    cleanup()

    // And on the project, against the workflow it belongs to.
    go('#/projects/local')
    await screen.findByText('gated')
    await screen.findAllByText(/waiting on you/)
    cleanup()

    // The approver is named, as the audit record requires.
    localStorage.setItem('wfx.actor', 'e2e-test')
    await api.approve(run.id, 'ship')
    await waitFor(async () => {
      expect((await api.run(run.id)).run.status).toBe('done')
    }, { timeout: 20_000, interval: 250 })
    const still = await api.runs({ status: ['awaiting_approval', 'needs_input'] })
    expect(still.map(r => r.id)).not.toContain(run.id)
  })

  test('a project carries a category: set at creation, changeable, and it leads its template list', async () => {
    go('#/projects')
    fireEvent.click(await screen.findByText('New project'))
    fireEvent.change(screen.getByPlaceholderText('my-project'), { target: { value: 'the-books' } })
    const pick = await screen.findByLabelText('Category')
    await waitFor(() => expect(within(pick).getAllByRole('option').length).toBeGreaterThan(5))
    fireEvent.change(pick, { target: { value: 'finance' } })
    fireEvent.click(screen.getByText('Create project'))
    await waitFor(() => expect(window.location.hash).toBe('#/projects/the-books'))
    expect((await api.project('the-books')).category).toBe('finance')
    cleanup()

    // Projects are grouped by category once there is more than one group.
    go('#/projects')
    await screen.findByText('Finance & Accounting')
    cleanup()

    // Changeable in place on the project's own page.
    go('#/projects/the-books')
    const chip = await screen.findByLabelText('Project category') as HTMLSelectElement
    await waitFor(() => expect(chip.value).toBe('finance'))
    fireEvent.change(chip, { target: { value: 'legal' } })
    await waitFor(async () => expect((await api.project('the-books')).category).toBe('legal'))
  })

  test("a workflow's page lists its runs, and each opens under the workflow", async () => {
    const run = await api.createRun('deterministic', { title: 'drill-down placement' })
    await waitFor(async () => {
      const d = await api.run(run.id)
      expect(d.run.status).toBe('done')
    }, { timeout: 20_000, interval: 250 })

    go('#/projects/local/deterministic')
    // A workflow that has run opens on its runs, named by what they are about.
    await screen.findByText('drill-down placement')
    expect(screen.getByRole('tab', { name: /History/ }).getAttribute('aria-selected')).toBe('true')
    cleanup()

    go(`#/projects/local/deterministic/runs/${run.id}`)
    await screen.findByText('1. greet')
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
  // list says it carries files, and the workflow's page names every one —
  // including the README, which the platform has no idea about and carries anyway.
  test("a project's workflows list the files that travel with them", async () => {
    go('#/projects/local')
    await screen.findByText('carrier')
    await screen.findByText('+ 3 files')
    cleanup()

    go('#/projects/local/carrier')
    fireEvent.click(await screen.findByRole('tab', { name: /Steps/ }))
    await screen.findByText('greet.sh')
    await screen.findByText('lib/phrase.sh')
    await screen.findByText('README.md')
    // With sizes, so "is this the whole thing" is answerable from the list.
    expect(screen.getAllByText(/\d+ B$/).length).toBeGreaterThan(0)
    cleanup()

    // And a flat one-file workflow says so rather than showing nothing.
    go('#/projects/local/deterministic')
    fireEvent.click(await screen.findByRole('tab', { name: /Steps/ }))
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
    fireEvent.click(await screen.findByRole('tab', { name: /Memory/ }))
    await screen.findByText('last_id')
    await screen.findByText('4120')
    // The global namespace is shared, so it shows here too — labelled, because
    // these are four separate namespaces and which one a value is in matters.
    await screen.findByText('tier')
    expect(screen.getAllByText('workflow').length).toBeGreaterThan(0)
    expect(screen.getAllByText('global').length).toBeGreaterThan(0)
  })

  test('every page renders without throwing', async () => {
    for (const route of ['#/projects', '#/projects/local', '#/projects/local/deterministic', '#/projects/local/+new', '#/projects/local/+template', '#/projects/local/deterministic/edit', '#/projects/local/deterministic/run', '#/workflows/new', '#/templates', '#/skills', '#/workers', '#/system']) {
      const { container, unmount } = go(route)
      // An app that threw renders nothing; this is the cheap general detector
      // for the whole class of bug.
      await waitFor(() => expect(container.querySelector('.top')).toBeTruthy())
      unmount()
    }
  })
})

describe('login state', () => {
  test('the solo install says auth is absent, and shows no login chrome', async () => {
    // The test server is the solo case: loopback, no users. whoami answers
    // rather than 401ing, and the answer is "absent", not "denied".
    const who = await api.whoami()
    expect(who.authenticated).toBe(false)
    expect(who.loopback).toBe(true)

    const { container } = go('#/projects')
    await waitFor(() => expect(container.querySelector('.top')).toBeTruthy())
    // Nothing about signing in appears anywhere — this is the three-scale rule:
    // the feature is ABSENT at the smallest scale, not merely inactive.
    await waitFor(() => expect(screen.getByText(/toolnexus/)).toBeTruthy())
    expect(screen.queryByRole('alert')).toBeNull()
    expect(container.textContent).not.toMatch(/wfx login/)
    expect(container.textContent).not.toMatch(/sign in/i)
  })

  test('a 401 from any call explains, once, that login happens in the terminal', async () => {
    const seen: string[] = []
    const off = onUnauthenticated(m => seen.push(m))
    // Any call, not a login-specific one: the handling is central.
    await expect(api.run('no-such-run')).rejects.toThrow()
    expect(seen).toEqual([])   // 404 is not 401
    off()

    // And the panel the listener drives, rendered directly with a forced 401.
    const failing = () => Promise.resolve(new Response(
      JSON.stringify({ error: 'unauthenticated: run `wfx login --url <host>`' }),
      { status: 401, headers: { 'Content-Type': 'application/json' } },
    ))
    const restore = globalThis.fetch
    globalThis.fetch = failing as unknown as typeof fetch
    try {
      render(<Identity />)
      const panel = await screen.findByRole('alert')
      expect(panel.textContent).toContain(`wfx login --url ${location.origin}`)
    } finally {
      globalThis.fetch = restore
    }
  })
})
