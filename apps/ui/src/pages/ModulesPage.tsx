import { useCallback, useEffect, useState } from 'react'
import { modulesApi, type Module, type Profile, type WfxInfo } from '../modulesApi'
import DataTable, { type Column } from '../components/DataTable'
import { ago } from '../lib/time'

/** Modules: the CLIs wfnexus wraps (apl, gh, aos…) as commands and supervised
 *  services. The list says what is ready; a module's page installs its CLI on
 *  the server and stores its settings (secrets go into the env store and are
 *  never shown back). The wfx card on top is the platform itself. */
export default function ModulesPage({ name }: { name?: string }) {
  return name ? <ModuleDetail name={name} /> : <ModuleList />
}

const stateClass = (s: string) => s === 'running' ? 'done' : s === 'crashed' || s === 'silent' ? 'failed' : 'pending'

function WfxCard() {
  const [w, setW] = useState<WfxInfo>()
  const [err, setErr] = useState('')
  useEffect(() => { modulesApi.wfx().then(setW).catch(e => setErr(String(e.message || e))) }, [])
  if (err) return <div className="card" style={{ marginBottom: 16 }}><p className="muted">wfx: {err}</p></div>
  if (!w) return null
  const current = w.version.version
  const behind = !!w.latest && w.latest !== current && w.latest !== 'v' + current
  return (
    <div className="card" style={{ marginBottom: 16 }}>
      <div className="subhead"><h2>wfx</h2>
        <span className={`badge ${behind ? 'awaiting_approval' : 'done'}`}>{behind ? `update: ${w.latest}` : 'up to date'}</span></div>
      <div className="grid2">
        <p><span className="muted">server</span> <span className="mono">{current}</span>{w.version.commit ? <span className="muted"> · {w.version.commit.slice(0, 7)}</span> : null}</p>
        <p><span className="muted">latest</span> <span className="mono">{w.latest || 'unknown'}</span></p>
        <p><span className="muted">default provider</span> <span className="mono">{w.defaultProvider || '(built-in)'}</span></p>
        <p><span className="muted">channels</span> {w.channels.length ? w.channels.map(c => <span key={c.name} className="mono" style={{ marginRight: 8 }}>{c.name}{c.default ? '*' : ''}</span>) : <span className="muted">none</span>}</p>
      </div>
      <p className="muted" style={{ marginTop: 8 }}>Upgrade the server from your laptop (no sudo; rolls back with <span className="mono">wfx server rollback</span>):</p>
      <pre className="mono" style={{ userSelect: 'all' }}>{w.upgrade}</pre>
      <p className="muted">Default provider and channel are server settings: <span className="mono">WFX_DEFAULT_PROVIDER</span>, <span className="mono">WFX_DEFAULT_CHANNEL</span>.</p>
    </div>
  )
}

function ModuleList() {
  const [mods, setMods] = useState<Module[]>()
  const [errs, setErrs] = useState<string[]>([])
  const [err, setErr] = useState('')
  const load = useCallback(() => {
    modulesApi.list().then(r => { setMods(r.modules); setErrs(r.errors || []); setErr('') })
      .catch(e => setErr(String(e.message || e)))
  }, [])
  useEffect(() => { load(); const t = setInterval(load, 15_000); return () => clearInterval(t) }, [load])

  const columns: Column<Module>[] = [
    { key: 'name', header: 'Module', width: 160, value: m => m.name,
      cell: m => <a className="mono" style={{ fontWeight: 550 }} href={`#/modules/${encodeURIComponent(m.name)}`}>{m.name}</a> },
    { key: 'version', header: 'Version', width: 90, value: m => m.version || '', cell: m => <span className="mono">{m.version || '—'}</span> },
    { key: 'ready', header: 'Ready', width: 140, value: m => m.ready ? 1 : 0,
      cell: m => m.ready ? <span className="badge done">ready</span>
        : <span className="badge failed" title={[...m.clis.filter(c => !c.installed).map(c => c.name + ' not installed'), ...m.missing.map(x => x + ' unset')].join(', ')}>needs setup</span> },
    { key: 'cli', header: 'CLI', width: 200, value: m => m.clis.map(c => c.name).join(' '),
      cell: m => m.clis.length ? m.clis.map(c => <span key={c.name} className={`badge ${c.installed ? 'done' : 'failed'}`} title={c.version} style={{ marginRight: 4 }}>{c.name}</span>) : <span className="muted">—</span> },
    { key: 'commands', header: 'Commands', width: 90, align: 'right', value: m => Object.keys(m.commands || {}).length,
      cell: m => <span>{Object.keys(m.commands || {}).length}</span> },
    { key: 'services', header: 'Services', value: m => m.instances.length,
      cell: m => m.instances.length ? m.instances.map(s => <span key={s.key} style={{ marginRight: 8 }}>
        <span className={`badge ${stateClass(s.state)}`}>{s.module} · {s.account}</span> <span className="muted">{s.state}, beat {ago(s.serviceHeartbeat || s.lastHeartbeat)}, {s.watches.length} watch</span></span>)
        : <span className="muted">{Object.keys(m.services || {}).length ? 'not started' : '—'}</span> },
  ]
  return (
    <div>
      <div className="subhead"><h1>Modules</h1></div>
      <WfxCard />
      {err && <p className="error">{err}</p>}
      {errs.map(e => <p key={e} className="error">{e}</p>)}
      {mods && <DataTable rows={mods} columns={columns} getKey={m => m.name}
        onRowClick={m => { location.hash = `#/modules/${encodeURIComponent(m.name)}` }} empty="No modules installed." />}
    </div>
  )
}

function ModuleDetail({ name }: { name: string }) {
  const [m, setM] = useState<Module>()
  const [profiles, setProfiles] = useState<Profile[]>([])
  const [err, setErr] = useState('')
  const [log, setLog] = useState('')
  const [busy, setBusy] = useState('')
  const [vals, setVals] = useState<Record<string, string>>({})
  const [saved, setSaved] = useState('')
  const load = useCallback(() => {
    modulesApi.get(name).then(x => { setM(x); setErr('') }).catch(e => setErr(String(e.message || e)))
    modulesApi.profiles(name).then(p => setProfiles(p.profiles || [])).catch(() => setProfiles([]))
  }, [name])
  useEffect(load, [load])

  const install = (cli: string) => {
    if (!confirm(`Run ${name}'s install command for ${cli} on the server?`)) return
    setBusy(cli); setLog('')
    modulesApi.install(name, cli)
      .then(r => setLog((r.log || '') + (r.error ? `\n✗ ${r.error}` : '\n✓ done')))
      .catch(e => setLog('✗ ' + String(e.message || e)))
      .finally(() => { setBusy(''); load() })
  }
  const save = (key: string) => {
    const v = vals[key]
    if (!v) return
    modulesApi.configure(name, key, v)
      .then(() => { setVals(x => ({ ...x, [key]: '' })); setSaved(key); load() })
      .catch(e => setErr(String(e.message || e)))
  }

  if (err && !m) return <p className="error">{err}</p>
  if (!m) return <p className="muted">Loading…</p>
  const keys = [...(m.requires.env || []).map(k => ({ k, secret: false })), ...(m.requires.secrets || []).map(k => ({ k, secret: true }))]
  return (
    <div>
      <div className="subhead"><h1><a href="#/modules">Modules</a> / <span className="mono">{m.name}</span> <span className="muted mono">{m.version}</span></h1>
        <span className={`badge ${m.ready ? 'done' : 'failed'}`}>{m.ready ? 'ready' : 'needs setup'}</span></div>
      {m.description && <p className="muted" style={{ marginBottom: 16 }}>{m.description}</p>}
      {err && <p className="error">{err}</p>}

      <div className="card" style={{ marginBottom: 16 }}>
        <h2>Install</h2>
        {m.clis.length === 0 && <p className="muted">This module wraps no CLI.</p>}
        {m.clis.map(c => (
          <div key={c.name} style={{ display: 'flex', gap: 12, alignItems: 'center', margin: '6px 0' }}>
            <span className={`badge ${c.installed ? 'done' : 'failed'}`}>{c.installed ? 'installed' : 'missing'}</span>
            <span className="mono" style={{ fontWeight: 550 }}>{c.name}</span>
            <span className="muted mono">{c.version}</span>
            {c.install && <button className="small" disabled={!!busy} onClick={() => install(c.name)}>
              {busy === c.name ? 'Installing…' : c.installed ? 'Reinstall' : 'Install'}</button>}
            {c.install && <span className="muted mono" style={{ fontSize: 12 }}>{c.install}</span>}
          </div>))}
        {log && <pre className="mono" style={{ maxHeight: 260, overflow: 'auto', marginTop: 8 }}>{log}</pre>}
      </div>

      <div className="card" style={{ marginBottom: 16 }}>
        <h2>Configure</h2>
        {keys.length === 0 && <p className="muted">Nothing to configure.</p>}
        {keys.map(({ k, secret }) => {
          const set = !m.missing.includes(k)
          return (
            <div key={k} style={{ display: 'flex', gap: 12, alignItems: 'center', margin: '6px 0' }}>
              <span className="mono" style={{ minWidth: 180 }}>{k}</span>
              <span className={`badge ${set ? 'done' : 'failed'}`}>{set ? 'set' : 'unset'}</span>
              <input type={secret ? 'password' : 'text'} autoComplete="off" placeholder={secret ? 'secret — stored sealed, never shown' : 'value'}
                value={vals[k] || ''} onChange={e => setVals(x => ({ ...x, [k]: e.target.value }))} />
              <button className="small" disabled={!vals[k]} onClick={() => save(k)}>Save</button>
              {saved === k && <span className="muted">saved</span>}
            </div>)
        })}
        {profiles.length > 0 && <>
          <h3 style={{ marginTop: 12 }}>Profiles</h3>
          {profiles.map(p => <p key={p.name}><span className="mono">{p.name}</span>{p.default ? ' (default)' : ''} <span className="muted">→ account {p.account || '—'}</span></p>)}
        </>}
      </div>

      <div className="card" style={{ marginBottom: 16 }}>
        <h2>Services</h2>
        {Object.keys(m.services || {}).length === 0 && <p className="muted">No services.</p>}
        {Object.entries(m.services || {}).map(([sn, s]) => <p key={sn}><span className="mono">{m.name}.{sn}</span> <span className="muted">{s.description}</span></p>)}
        {m.instances.map(s => (
          <div key={s.key} style={{ margin: '6px 0' }}>
            <span className={`badge ${stateClass(s.state)}`}>{s.state}</span> <span className="mono">{s.module} · {s.account}</span>
            <span className="muted"> · supervisor beat {ago(s.lastHeartbeat)} · own beat {ago(s.serviceHeartbeat)} · {s.restarts} restarts · {s.watches.length} watches{s.owner ? ` · on ${s.owner}` : ''}</span>
            {s.lastError && <p className="error">{s.lastError}</p>}
          </div>))}
      </div>

      <div className="card">
        <h2>Commands</h2>
        {Object.entries(m.commands || {}).sort().map(([cn, c]) => (
          <p key={cn}><span className="mono" style={{ fontWeight: 550 }}>{m.name}.{cn}</span> <span className="muted">{c.description}</span>
            {c.inputs && <span className="muted mono" style={{ fontSize: 12 }}> ({Object.keys(c.inputs).join(', ')})</span>}</p>))}
      </div>
    </div>
  )
}
