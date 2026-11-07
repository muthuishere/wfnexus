import { Fragment, useCallback, useEffect, useState, type ReactNode } from 'react'
import { api, type Channel, type Subscription, type SubscriptionDelivery } from '../api'

/** A project's subscriptions (ADR 0026): which chat messages start which of
 *  its workflows. Lives inside the project drill-down — a subscription is
 *  listed under the project its target workflow belongs to.
 *
 *  A save is validated by the server: it comes back `active` (and starts from
 *  now — nothing older is ever answered) or `error` with the reason. */
export default function Subscriptions({ project, workflows }: { project: string; workflows: string[] }) {
  const [subs, setSubs] = useState<Subscription[]>()
  const [err, setErr] = useState('')
  const [editing, setEditing] = useState<Subscription | null>(null)
  const [open, setOpen] = useState<string>('')

  const load = useCallback(() => {
    api.subscriptions(project).then(s => { setSubs(Array.isArray(s) ? s : []); setErr('') })
      .catch(e => setErr(e instanceof Error ? e.message : String(e)))
  }, [project])
  useEffect(load, [load])

  const act = (name: string, a: 'pause' | 'resume' | 'validate') =>
    api.subscriptionAction(name, a).then(load).catch(e => setErr(String(e.message || e)))

  return (
    <div className="card">
      <div className="subhead">
        <h2>Subscriptions</h2>
        <button className="ghost small" onClick={() => setEditing(editing ? null : blank(workflows[0] || ''))}>
          {editing ? 'Cancel' : '+ subscription'}
        </button>
      </div>
      <p className="muted" style={{ marginBottom: 12 }}>
        A message in a chat starts one of this project's workflows. In a group, a run starts only when the message
        mentions us or replies to us, unless activation is <span className="mono">always</span>. Only senders on the
        allow list can start a run; an empty list means nobody.
      </p>
      {err && <div className="banner err">{err}</div>}
      {editing && <SubForm initial={editing} workflows={workflows}
        onDone={() => { setEditing(null); load() }} onError={setErr} />}
      {!subs ? <div className="muted">loading…</div>
        : subs.length === 0 ? <div className="muted">No subscriptions for this project.</div>
          : <table className="rows"><tbody>
            {subs.map(s => (<Fragment key={s.name}>
              <tr>
                <td className="mono" style={{ fontWeight: 550 }}>
                  <a href="#" onClick={e => { e.preventDefault(); setOpen(open === s.name ? '' : s.name) }}>{s.name}</a>
                </td>
                <td><span className="pill">{s.status}</span></td>
                <td className="mono muted" style={{ fontSize: 'var(--fs-2xs)' }}>{s.channel} / {s.chat}</td>
                <td className="mono">{s.workflow}</td>
                <td className="muted">{s.effectiveMode} · every {s.interval || '1m'} · {(s.allow || []).length} allowed</td>
                <td style={{ whiteSpace: 'nowrap' }}>
                  {s.status === 'paused'
                    ? <button className="ghost small" onClick={() => act(s.name, 'resume')}>Resume</button>
                    : <button className="ghost small" onClick={() => act(s.name, 'pause')}>Pause</button>}
                  <button className="ghost small" onClick={() => act(s.name, 'validate')}>Validate</button>
                  <button className="ghost small" onClick={() => setEditing(s)}>Edit</button>
                  <button className="ghost small danger-text" onClick={() => {
                    if (!confirm(`Remove subscription ${s.name}?`)) return
                    api.deleteSubscription(s.name).then(load).catch(e => setErr(String(e.message || e)))
                  }}>Remove</button>
                </td>
              </tr>
              {(s.error || s.problem) && (
                <tr key={s.name + ':err'}><td colSpan={6} className="danger-text" style={{ fontSize: 'var(--fs-2xs)' }}>{s.error || s.problem}</td></tr>)}
              {!s.error && !s.problem && s.warning && (
                <tr key={s.name + ':warn'}><td colSpan={6} style={{ fontSize: 'var(--fs-2xs)' }}>{s.warning}</td></tr>)}
              {open === s.name && <tr key={s.name + ':d'}><td colSpan={6}><Deliveries name={s.name} /></td></tr>}
            </Fragment>))}
          </tbody></table>}
    </div>
  )
}

function blank(workflow: string): Subscription {
  return { name: '', channel: '', chat: '', workflow, allow: [], events: ['message'], interval: '1m', responsePolicy: 'draft' }
}

const list = (v: string) => v.split(',').map(x => x.trim()).filter(Boolean)

function SubForm({ initial, workflows, onDone, onError }: {
  initial: Subscription; workflows: string[]; onDone: () => void; onError: (e: string) => void
}) {
  const [s, setS] = useState<Subscription>(initial)
  const [channels, setChannels] = useState<Channel[]>([])
  useEffect(() => { api.channels().then(d => setChannels(d.channels)).catch(() => setChannels([])) }, [])
  const set = (k: keyof Subscription, v: any) => setS(prev => ({ ...prev, [k]: v }))
  const editing = !!initial.name
  const field = (label: string, el: ReactNode) => (
    <label style={{ display: 'block', marginBottom: 8 }}><span className="muted" style={{ display: 'block' }}>{label}</span>{el}</label>)
  return (
    <form style={{ marginBottom: 16 }} onSubmit={e => {
      e.preventDefault()
      api.saveSubscription(s).then(onDone).catch(err => onError(String(err.message || err)))
    }}>
      {field('Name', <input value={s.name} disabled={editing} onChange={e => set('name', e.target.value)} required />)}
      {field('Channel', <select value={s.channel} onChange={e => set('channel', e.target.value)} required>
        <option value="">—</option>
        {channels.map(c => <option key={c.name} value={c.name}>{c.name}</option>)}
      </select>)}
      {field('Chat (a WhatsApp JID, or * for every chat)', <input value={s.chat} onChange={e => set('chat', e.target.value)} required />)}
      {field('Workflow', <select value={s.workflow} onChange={e => set('workflow', e.target.value)} required>
        {workflows.map(w => <option key={w} value={w}>{w}</option>)}
      </select>)}
      {field('Events (comma separated: message, reaction, updated, deleted)',
        <input value={(s.events || []).join(', ')} onChange={e => set('events', list(e.target.value))} />)}
      {field('Interval', <select value={s.interval || '1m'} onChange={e => set('interval', e.target.value)}>
        {['30s', '1m', '2m'].map(v => <option key={v}>{v}</option>)}
      </select>)}
      {field('Mode', <select value={s.mode || ''} onChange={e => set('mode', e.target.value)}>
        <option value="">default (exclusive when it replies)</option>
        {['fanout', 'exclusive', 'observe'].map(v => <option key={v}>{v}</option>)}
      </select>)}
      {field('Activation (groups)', <select value={s.activation || ''} onChange={e => set('activation', e.target.value)}>
        <option value="">mention (default)</option><option value="always">always</option>
      </select>)}
      {field('Allow (sender ids, comma separated; * for everyone)',
        <input value={(s.allow || []).join(', ')} onChange={e => set('allow', list(e.target.value))} />)}
      {field('Response policy', <select value={s.responsePolicy || 'draft'} onChange={e => set('responsePolicy', e.target.value)}>
        {['draft', 'approval', 'auto', 'none'].map(v => <option key={v}>{v}</option>)}
      </select>)}
      {field('Instructions', <textarea value={s.instructions || ''} rows={3} onChange={e => set('instructions', e.target.value)} />)}
      <button type="submit">{editing ? 'Save' : 'Add subscription'}</button>
    </form>
  )
}

function Deliveries({ name }: { name: string }) {
  const [rows, setRows] = useState<SubscriptionDelivery[]>()
  useEffect(() => { api.subscription(name).then(d => setRows(d.deliveries || [])).catch(() => setRows([])) }, [name])
  if (!rows) return <div className="muted">loading…</div>
  if (rows.length === 0) return <div className="muted">Nothing routed yet.</div>
  return (<div>
    {rows.map(d => (
      <div key={d.msgId} style={{ fontSize: 'var(--fs-2xs)' }}>
        <span className="muted mono">{new Date(d.createdAt).toLocaleString()}</span>{' '}
        <span className="mono">{d.msgId}</span> <span className="pill">{d.outcome}</span>
        {d.runId && <a href={`/runs/${d.runId}`} style={{ marginLeft: 6 }}>run</a>}
      </div>))}
  </div>)
}
