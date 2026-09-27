import { describe, test, expect, beforeAll, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, waitFor, fireEvent, within } from '@testing-library/react'
import Changes, { ProposalView, ReviewBadge, ProposedNotice } from './components/Changes'
import ConnectRepo from './components/ConnectRepo'
import Checks, { checksFor } from './components/Checks'
import { api } from './api'
import { isProposal, prLabel, type Proposal, type ProposalDetail } from './lifecycle'

// The lifecycle API (proposals, review, drift) is being built on the server
// alongside this UI, so the test server does not answer it yet. Those routes —
// and only those — are answered here with the contract's exact shapes; every
// other request still reaches the real server. When the server lands, delete
// the stub and these become end-to-end like the rest.
const thrown: unknown[] = []
beforeAll(() => {
  window.addEventListener('error', e => thrown.push(e.error ?? e.message))
  window.addEventListener('unhandledrejection', e => thrown.push(e.reason))
})

const pending: ProposalDetail = {
  id: 'p1', project: 'local', workflow: 'deterministic', kind: 'edit', branch: 'wfx/edit-deterministic',
  pr_url: 'https://github.com/acme/repo/pull/42', status: 'pending',
  review: { verdict: 'approve', reason: 'adds an output contract, nothing else', score: 0.91 },
  diff: '--- a/x.yaml\n+++ b/x.yaml\n@@ -1 +1 @@\n-old: 1\n+new: 2',
}
const deletion: Proposal = {
  id: 'p2', project: 'local', workflow: 'failing', kind: 'delete', branch: 'wfx/delete-failing',
  status: 'pending', review: { verdict: 'unreviewed', reason: '', score: 0 },
}

let calls: Array<{ method: string; url: string; body?: any }> = []
let state: Record<string, ProposalDetail>
let drift: Array<{ workflow: string; path: string }>
let passThrough: typeof fetch
const json = (b: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(b), { status, headers: { 'Content-Type': 'application/json' } }))

beforeEach(() => {
  calls = []
  state = { p1: { ...pending }, p2: { ...deletion, diff: '' } }
  drift = [{ workflow: 'hand-made', path: '.wfx/workflows/hand-made.yaml' }]
  passThrough = globalThis.fetch
  globalThis.fetch = ((input: any, init?: any) => {
    const url = String(input), method = init?.method || 'GET'
    const body = init?.body ? JSON.parse(init.body) : undefined
    let m
    if ((m = url.match(/^\/api\/projects\/([^/]+)\/proposals$/))) { calls.push({ method, url }); return json(Object.values(state)) }
    if ((m = url.match(/^\/api\/proposals\/([^/]+)\/(approve|reject)$/))) {
      calls.push({ method, url, body })
      state[m[1]] = { ...state[m[1]], status: m[2] === 'approve' ? 'approved' : 'rejected' }
      return json(state[m[1]])
    }
    if ((m = url.match(/^\/api\/proposals\/([^/]+)$/))) { calls.push({ method, url }); return json(state[m[1]]) }
    if (url.match(/\/drift\/propose$/)) {
      calls.push({ method, url }); drift = []
      return json({ ...deletion, id: 'p3', kind: 'create', workflow: 'hand-made', pr_url: 'https://github.com/acme/repo/pull/43' })
    }
    if (url.match(/\/drift$/)) { calls.push({ method, url }); return json(drift) }
    return passThrough(input, init)
  }) as typeof fetch
})
afterEach(() => { globalThis.fetch = passThrough; cleanup(); expect(thrown).toEqual([]) })

describe('the lifecycle contract', () => {
  test('a save that answers with a proposal is told apart from one that wrote', () => {
    expect(isProposal(pending)).toBe(true)
    expect(isProposal({ name: 'x', path: '/y' })).toBe(false)
    expect(prLabel(pending)).toBe('PR #42')
    // No PR yet: the branch is the only honest name for it.
    expect(prLabel(deletion)).toBe('wfx/delete-failing')
  })

  test('"Proposed change: PR #n", never "Saved"', () => {
    render(<ProposedNotice proposal={pending} />)
    expect(screen.getByRole('status').textContent).toMatch(/Proposed change: PR #42/)
    expect(screen.getByRole('status').textContent).not.toMatch(/Saved/)
  })

  test('the review badge says unreviewed plainly, and shows the score when there is one', () => {
    const { container: a } = render(<ReviewBadge review={pending.review} />)
    expect(a.textContent).toBe('classifier: approve · 0.91')
    const { container: u } = render(<ReviewBadge review={deletion.review} />)
    expect(u.textContent).toBe('not reviewed')
    const { container: r } = render(<ReviewBadge review={{ verdict: 'reject', reason: 'drops a gate', score: 0.2 }} />)
    expect(r.querySelector('.badge.failed')).toBeTruthy()
  })
})

describe('the Changes tab', () => {
  test('lists proposals and drift; a row opens its diff', async () => {
    render(<Changes project="local" />)
    const table = await waitFor(() => { const t = document.querySelector('table.proposals'); expect(t).toBeTruthy(); return t as HTMLElement })
    expect(within(table).getByText('PR #42')).toBeTruthy()
    expect(within(table).getByText('not reviewed')).toBeTruthy()
    await screen.findByText('Untracked workflows')

    fireEvent.click(within(table).getByText('deterministic'))
    await waitFor(() => expect(document.querySelector('pre.diff .add')?.textContent).toBe('+new: 2'))
    expect(document.querySelector('pre.diff .del')?.textContent).toBe('-old: 1')
  })

  test("a workflow's Changes are only that workflow's", async () => {
    render(<Changes project="local" workflow="failing" />)
    await screen.findByText('wfx/delete-failing')
    expect(screen.queryByText('PR #42')).toBeNull()
    // Drift is a project-level thing; it is not repeated per workflow.
    expect(screen.queryByText('Untracked workflows')).toBeNull()
  })

  test('Propose commit turns drift into a proposal', async () => {
    render(<Changes project="local" />)
    fireEvent.click(await screen.findByText('Propose commit'))
    await screen.findByText(/Proposed change:/)
    expect(calls.some(c => c.method === 'POST' && c.url === '/api/projects/local/drift/propose')).toBe(true)
  })
})

describe('deciding a proposal', () => {
  test('Approve posts, and the proposal is then decided', async () => {
    render(<ProposalView id="p1" />)
    fireEvent.click(await screen.findByText('Approve'))
    await screen.findByText('approved')
    expect(calls.find(c => c.url === '/api/proposals/p1/approve')?.method).toBe('POST')
    expect((screen.getByText('Approve') as HTMLButtonElement).disabled).toBe(true)
  })

  test('Reject needs a reason, and sends it', async () => {
    render(<ProposalView id="p1" />)
    fireEvent.click(await screen.findByText('Reject'))
    const go = screen.getByText('Reject change') as HTMLButtonElement
    expect(go.disabled).toBe(true)
    fireEvent.change(screen.getByLabelText('Why reject it?'), { target: { value: 'removes the approval gate' } })
    fireEvent.click(go)
    await screen.findByText('rejected')
    expect(calls.find(c => c.url === '/api/proposals/p1/reject')?.body).toEqual({ reason: 'removes the approval gate' })
  })
})

describe('onboarding', () => {
  test('Connect a repo asks for a git URL and a branch, and needs the URL', () => {
    render(<ConnectRepo />)
    expect(screen.getByLabelText('Git URL')).toBeTruthy()
    expect(screen.getByLabelText('Branch')).toBeTruthy()
    expect((screen.getByText('Connect') as HTMLButtonElement).disabled).toBe(true)
    fireEvent.change(screen.getByLabelText('Git URL'), { target: { value: 'https://github.com/acme/repo.git' } })
    expect((screen.getByText('Connect') as HTMLButtonElement).disabled).toBe(false)
  })

  test('a repo with no workflows is offered starter templates', async () => {
    const real = globalThis.fetch
    globalThis.fetch = ((input: any, init?: any) =>
      String(input) === '/api/projects' && init?.method === 'POST'
        ? json({ name: 'acme', workflows: [] })
        : real(input, init)) as typeof fetch
    render(<ConnectRepo />)
    fireEvent.change(screen.getByLabelText('Git URL'), { target: { value: 'https://github.com/acme/repo.git' } })
    fireEvent.click(screen.getByText('Connect'))
    await screen.findByText(/has no/)
    await waitFor(() => expect(document.querySelectorAll('.starter').length).toBeGreaterThan(0))
  })
})

describe('the Checks tab', () => {
  test("a real workflow's checks, from its definition and runs", async () => {
    const w = (await api.workflows()).find(x => x.name === 'deterministic')!
    const list = checksFor(w, [])
    expect(list.find(c => c.id === 'loads')?.ok).toBe(true)
    // Never run is not a pass.
    expect(list.find(c => c.id === 'recent')?.ok).toBe('warn')
    const failing = checksFor(w, [{ status: 'failed' }, { status: 'failed' }] as any)
    expect(failing.find(c => c.id === 'recent')?.ok).toBe(false)

    const { container } = render(<Checks workflow={w} runs={[]} />)
    expect(container.querySelectorAll('tr[data-check]').length).toBe(list.length)
  })
})
