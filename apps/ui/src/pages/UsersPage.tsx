import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { api, type Role, type User } from '../api'
import DataTable, { type Column } from '../components/DataTable'

/** Users (ADR 0025): add, disable, reset a password, change a role.
 *
 *  Every action is a call the server authorizes (admin:read / admin:write), so
 *  a person without the permission sees the server's refusal, not a hidden
 *  button. The server also refuses to disable or demote the last admin.
 *  A password the server generates is shown ONCE, here, and never again. */
export default function UsersPage() {
  const [users, setUsers] = useState<User[]>()
  const [roles, setRoles] = useState<Role[]>([])
  const [err, setErr] = useState('')
  const [secret, setSecret] = useState<{ name: string; password: string } | null>(null)

  const load = useCallback(() => {
    api.users().then(u => { setUsers(u); setErr('') }, e => setErr(msg(e)))
    api.roles().then(setRoles, () => {})
  }, [])
  useEffect(load, [load])

  const act = (p: Promise<unknown>) => p.then(load, e => setErr(msg(e)))
  const reset = (name: string) => {
    if (!confirm(`Reset ${name}'s password? Their current password stops working.`)) return
    api.resetPassword(name).then(r => {
      if (r.password) setSecret({ name, password: r.password })
      load()
    }, e => setErr(msg(e)))
  }

  const columns: Column<User>[] = [
    { key: 'name', header: 'User', width: 180, value: u => u.name,
      cell: u => <span className="mono" style={{ fontWeight: 550 }}>{u.name}</span> },
    { key: 'role', header: 'Role', width: 160, value: u => u.role,
      cell: u => (
        <select aria-label={`Role of ${u.name}`} value={u.role}
          onChange={e => act(api.updateUser({ ...u, role: e.target.value }))}>
          {roleNames(roles, u.role).map(r => <option key={r} value={r}>{r}</option>)}
        </select>
      ) },
    { key: 'project', header: 'Project', width: 130, value: u => u.project,
      cell: u => <span className="muted">{u.project || 'all'}</span> },
    { key: 'status', header: 'Status', width: 110, value: u => (u.disabled ? 'disabled' : 'active'),
      cell: u => <span className={`badge ${u.disabled ? 'failed' : 'done'}`}>{u.disabled ? 'disabled' : 'active'}</span> },
    { key: 'password', header: 'Password', width: 130,
      value: u => (!u.hasPassword ? 'none' : u.mustChange ? 'must change' : 'set'),
      cell: u => <span className="muted">{!u.hasPassword ? 'none' : u.mustChange ? 'must change' : 'set'}</span> },
    { key: 'actions', header: '', width: 230, align: 'right', sortable: false,
      cell: u => (
        <span style={{ display: 'inline-flex', gap: 6 }}>
          <button className="ghost small" onClick={e => { e.stopPropagation(); reset(u.name) }}>Reset password</button>
          <button className="ghost small" onClick={e => { e.stopPropagation(); act(api.updateUser({ ...u, disabled: !u.disabled })) }}>
            {u.disabled ? 'Enable' : 'Disable'}
          </button>
        </span>
      ) },
  ]

  return (
    <div>
      <div className="subhead"><h1>Users</h1></div>
      <p className="muted">People who can sign in. A new user gets a one-time password and sets their own at first sign-in.</p>
      {err ? <div role="status" className="danger-text" style={{ margin: '8px 0' }}>{err}</div> : null}
      {secret ? (
        <div className="card" role="status" style={{ margin: '12px 0' }}>
          <div>One-time password for <b>{secret.name}</b> — shown once. Send it to them privately:</div>
          <div className="mono" style={{ fontSize: 18, margin: '8px 0' }}>{secret.password}</div>
          <button className="ghost small" onClick={() => setSecret(null)}>Done</button>
        </div>
      ) : null}
      <AddUser roles={roles} onAdded={(name, password) => { if (password) setSecret({ name, password }); load() }} onError={e => setErr(e)} />
      {users ? <DataTable rows={users} columns={columns} getKey={u => u.name} /> : <p className="muted">Loading…</p>}
    </div>
  )
}

function AddUser({ roles, onAdded, onError }: {
  roles: Role[]; onAdded: (name: string, password?: string) => void; onError: (e: string) => void
}) {
  const [name, setName] = useState('')
  const [role, setRole] = useState('viewer')
  const [project, setProject] = useState('')
  const submit = (e: FormEvent) => {
    e.preventDefault()
    api.createUser({ name: name.trim(), role, project: project.trim() })
      .then(() => api.resetPassword(name.trim()))
      .then(r => { onAdded(name.trim(), r.password); setName(''); setProject('') }, err => onError(msg(err)))
  }
  return (
    <form onSubmit={submit} aria-label="Add a user" style={{ display: 'flex', gap: 8, alignItems: 'end', flexWrap: 'wrap', margin: '12px 0' }}>
      <label>Username<input name="name" value={name} onChange={e => setName(e.target.value)} required /></label>
      <label>Role
        <select name="role" value={role} onChange={e => setRole(e.target.value)}>
          {roleNames(roles, role).map(r => <option key={r} value={r}>{r}</option>)}
        </select>
      </label>
      <label>Project (optional)<input name="project" value={project} onChange={e => setProject(e.target.value)} placeholder="all" /></label>
      <button type="submit" disabled={!name.trim()}>Add user</button>
    </form>
  )
}

function roleNames(roles: Role[], current: string): string[] {
  const names = roles.map(r => r.name)
  return names.includes(current) ? names : [current, ...names]
}

function msg(e: unknown) { return e instanceof Error ? e.message : String(e) }
