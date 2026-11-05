import { useCallback, useEffect, useState } from 'react'
import { api, type Role } from '../api'

/** Roles (ADR 0025): edit what each existing role may do.
 *
 *  A role is a row of permission strings, read by the server on every request,
 *  so a save here changes the very next request. The vocabulary below mirrors
 *  apps/api/internal/api/authz.go; a permission the row holds that is not in
 *  it is kept and shown, never silently dropped. */
const VOCAB = [
  'workflows:read', 'workflows:write', 'runs:read', 'runs:write',
  'registry:read', 'registry:write', 'projects:read', 'projects:write',
  'env:read', 'env:write', 'state:read', 'state:write',
  'workers:read', 'workers:write', 'bundles:read', 'publish',
  'admin:read', 'admin:write',
]

export default function RolesPage() {
  const [roles, setRoles] = useState<Role[]>()
  const [err, setErr] = useState('')
  const load = useCallback(() => {
    api.roles().then(r => { setRoles([...r].sort((a, b) => a.name.localeCompare(b.name))); setErr('') },
      e => setErr(e instanceof Error ? e.message : String(e)))
  }, [])
  useEffect(load, [load])

  return (
    <div>
      <div className="subhead"><h1>Roles</h1></div>
      <p className="muted">What each role may do. <span className="mono">*</span> is everything. Changes apply on the next request.</p>
      {err ? <div role="status" className="danger-text" style={{ margin: '8px 0' }}>{err}</div> : null}
      {roles ? roles.map(r => <RoleEditor key={r.name} role={r} onSaved={load} onError={setErr} />) : <p className="muted">Loading…</p>}
    </div>
  )
}

function RoleEditor({ role, onSaved, onError }: { role: Role; onSaved: () => void; onError: (e: string) => void }) {
  const [perms, setPerms] = useState<string[]>(role.permissions)
  useEffect(() => setPerms(role.permissions), [role.permissions])
  const all = perms.includes('*')
  const extra = perms.filter(p => p !== '*' && !VOCAB.includes(p))
  const dirty = [...perms].sort().join() !== [...role.permissions].sort().join()
  const toggle = (p: string) => setPerms(cur => cur.includes(p) ? cur.filter(x => x !== p) : [...cur, p])
  const save = () => api.saveRole(role.name, perms).then(onSaved, e => onError(e instanceof Error ? e.message : String(e)))

  return (
    <section className="card" aria-label={`Role ${role.name}`} style={{ margin: '12px 0' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 8 }}>
        <h2 className="mono" style={{ margin: 0 }}>{role.name}</h2>
        <label style={{ display: 'inline-flex', gap: 6, alignItems: 'center' }}>
          <input type="checkbox" checked={all} onChange={() => toggle('*')} /> everything (*)
        </label>
        <span style={{ flex: 1 }} />
        <button className="small" disabled={!dirty} onClick={save}>Save</button>
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(170px, 1fr))', gap: 4, opacity: all ? 0.5 : 1 }}>
        {[...VOCAB, ...extra].map(p => (
          <label key={p} className="mono" style={{ display: 'inline-flex', gap: 6, alignItems: 'center', fontSize: 'var(--fs-sm)' }}>
            <input type="checkbox" aria-label={`${role.name} ${p}`} checked={perms.includes(p)} disabled={all} onChange={() => toggle(p)} /> {p}
          </label>
        ))}
      </div>
    </section>
  )
}
