import { afterEach, describe, expect, test } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import Identity from './components/Identity'
import UsersPage from './pages/UsersPage'
import RolesPage from './pages/RolesPage'

// ADR 0025 — password sign-in, the must-change screen, and the admin pages.
// Each test fakes fetch: these are the browser's halves of rules the Go tests
// prove on the server.

type Call = { method: string; url: string; body: any }
const restore = globalThis.fetch
afterEach(() => { globalThis.fetch = restore; cleanup() })

function fake(routes: Record<string, (body: any) => [number, unknown]>): Call[] {
  const calls: Call[] = []
  globalThis.fetch = ((url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    const body = init?.body ? JSON.parse(String(init.body)) : undefined
    calls.push({ method, url, body })
    const h = routes[`${method} ${url}`]
    const [status, out] = h ? h(body) : [400, { error: 'authorization_pending' }]
    return Promise.resolve(new Response(JSON.stringify(out), { status, headers: { 'Content-Type': 'application/json' } }))
  }) as unknown as typeof fetch
  return calls
}

describe('password sign-in', () => {
  test('off loopback the card has a username and password form that posts to /api/auth/password', async () => {
    const calls = fake({
      'GET /api/whoami': () => [200, { authenticated: false, loopback: false, authRequired: true }],
      'POST /api/device/code': () => [200, { device_code: 'dc', user_code: 'BCDF-GHJK', interval: 60, expires_in: 900 }],
      'POST /api/auth/password': () => [401, { error: 'invalid username or password' }],
    })
    render(<Identity />)
    const form = await screen.findByRole('form', { name: 'Sign in with a password' })
    fireEvent.change(form.querySelector('input[name=username]')!, { target: { value: 'ana' } })
    fireEvent.change(form.querySelector('input[name=password]')!, { target: { value: 'wrong-password' } })
    fireEvent.submit(form)
    await screen.findByText('invalid username or password')
    expect(calls.find(c => c.url === '/api/auth/password')?.body).toEqual({ username: 'ana', password: 'wrong-password' })
    // The device grant is still offered beside it.
    await waitFor(() => expect(document.body.textContent).toContain('wfx login --approve BCDF-GHJK'))
    // The password field is cleared after a failure.
    expect((form.querySelector('input[name=password]') as HTMLInputElement).value).toBe('')
  })

  test('a user who must change their password sees only the change-password screen', async () => {
    const calls = fake({
      'GET /api/whoami': () => [200, { authenticated: true, kind: 'user', name: 'admin', role: 'admin', authRequired: true, mustChange: true }],
      'POST /api/auth/password/change': () => [200, { changed: true }],
    })
    render(<Identity />)
    const form = await screen.findByRole('form', { name: 'Change password' })
    expect(screen.getByRole('alert').textContent).toContain('Set a new password')
    const submit = screen.getByRole('button', { name: 'Change password' }) as HTMLButtonElement
    fireEvent.change(form.querySelector('input[name=current]')!, { target: { value: 'one-time-pass-123' } })
    fireEvent.change(form.querySelector('input[name=new]')!, { target: { value: 'short' } })
    fireEvent.change(form.querySelector('input[name=again]')!, { target: { value: 'short' } })
    expect(screen.getByText('At least 12 characters.')).toBeTruthy()
    expect(submit.disabled).toBe(true)
    fireEvent.change(form.querySelector('input[name=new]')!, { target: { value: 'my-own-password-1' } })
    fireEvent.change(form.querySelector('input[name=again]')!, { target: { value: 'my-own-password-1' } })
    expect(submit.disabled).toBe(false)
    fireEvent.submit(form)
    await waitFor(() => expect(calls.some(c => c.url === '/api/auth/password/change')).toBe(true))
    expect(calls.find(c => c.url === '/api/auth/password/change')?.body)
      .toEqual({ current: 'one-time-pass-123', new: 'my-own-password-1' })
  })
})

describe('admin pages', () => {
  const ana = { id: '1', name: 'ana', displayName: '', role: '', project: '', disabled: false, mustChange: false, hasPassword: true, createdAt: '' }
  const roles = [{ name: 'admin', permissions: ['*'] }, { name: 'viewer', permissions: ['runs:read'] }]

  test('Users: reset shows the one-time password once; disable and role change PUT the user', async () => {
    const calls = fake({
      'GET /api/users': () => [200, [ana]],
      'GET /api/roles': () => [200, roles],
      'POST /api/users/ana/password': () => [200, { password: 'OneTimePass12345678', mustChange: true }],
      'PUT /api/users/ana': b => [200, { ...ana, ...b }],
    })
    globalThis.confirm = () => true
    render(<UsersPage />)
    await screen.findByText('ana')
    fireEvent.click(screen.getByRole('button', { name: 'Reset password' }))
    await screen.findByText('OneTimePass12345678')

    fireEvent.click(screen.getByRole('button', { name: 'Disable' }))
    await waitFor(() => expect(calls.some(c => c.method === 'PUT')).toBe(true))
    expect(calls.find(c => c.method === 'PUT')?.body).toMatchObject({ disabled: true, role: '' })

    fireEvent.change(screen.getByLabelText('Role of ana'), { target: { value: 'superadmin' } })
    await waitFor(() => expect(calls.filter(c => c.method === 'PUT').length).toBe(2))
    expect(calls.filter(c => c.method === 'PUT')[1].body).toMatchObject({ role: 'superadmin', disabled: false })
  })

  test('Users: adding a user creates it and then sets a one-time password', async () => {
    const calls = fake({
      'GET /api/users': () => [200, []],
      'GET /api/roles': () => [200, roles],
      'POST /api/users': () => [201, { user: ana }],
      'POST /api/users/bo/password': () => [200, { password: 'BoOneTimePass123456', mustChange: true }],
    })
    render(<UsersPage />)
    const form = await screen.findByRole('form', { name: 'Add a user' })
    fireEvent.change(form.querySelector('input[name=name]')!, { target: { value: 'bo' } })
    fireEvent.submit(form)
    await screen.findByText('BoOneTimePass123456')
    expect(calls.find(c => c.method === 'POST' && c.url === '/api/users')?.body).toMatchObject({ name: 'bo', role: '' })
  })

  test('Roles: toggling a permission and saving PUTs the role row', async () => {
    const calls = fake({
      'GET /api/roles': () => [200, roles],
      'PUT /api/roles/viewer': b => [200, { name: 'viewer', ...b }],
    })
    render(<RolesPage />)
    const box = await screen.findByLabelText('viewer runs:write')
    fireEvent.click(box)
    const section = screen.getByRole('region', { name: 'Role viewer' })
    fireEvent.click(section.querySelector('button')!)
    await waitFor(() => expect(calls.some(c => c.method === 'PUT')).toBe(true))
    expect(calls.find(c => c.method === 'PUT')?.body).toEqual({ permissions: ['runs:read', 'runs:write'] })
  })
})
