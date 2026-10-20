import { useEffect, useState } from 'react'
import { api, type Channel, type ChannelMessage } from '../api'

/** Messages received on channels (ADR 0023), newest first, with what each did:
 *  `resolved` answered a pause, `recorded` was a reply that was not allowed to
 *  (sender not on the channel's allow list), `triggered` started a run.
 *
 *  Given a run, it shows that run's replies and renders nothing when there are
 *  none — so a run page that never touched a channel is unchanged. */
export function ChannelMessages({ run, channel, title = 'Messages' }: { run?: string; channel?: string; title?: string }) {
  const [rows, setRows] = useState<ChannelMessage[]>()
  useEffect(() => {
    let live = true
    api.channelMessages({ run, channel, limit: 50 })
      .then(r => { if (live) setRows(Array.isArray(r) ? r : []) })
      .catch(() => { if (live) setRows([]) })
    return () => { live = false }
  }, [run, channel])
  if (!rows || (run && rows.length === 0)) return null
  return (
    <div className="card">
      <h3>{title}</h3>
      {rows.length === 0 && <p className="muted">Nothing received yet.</p>}
      {rows.map(m => (
        <div key={m.id} style={{ marginBottom: 6 }}>
          <span className="muted mono" style={{ fontSize: 11.5 }}>{new Date(m.receivedAt).toLocaleString()} · {m.channel}</span>{' '}
          <b>{m.name || m.from}</b>: {m.text}
          {m.outcome && <span className="pill" style={{ marginLeft: 6 }}>{m.outcome}</span>}
          {!run && m.runId && <a href={`/runs/${m.runId}`} style={{ marginLeft: 6 }}>run</a>}
        </div>))}
    </div>
  )
}

/** The channels this server sends and receives on — the System page's view.
 *  Channels are configured in registries.json and config.yaml, not here. */
export default function Channels() {
  const [doc, setDoc] = useState<{ default: string; channels: Channel[] }>()
  useEffect(() => { api.channels().then(setDoc).catch(() => setDoc({ default: '', channels: [] })) }, [])
  if (!doc) return null
  return (<>
    <div className="card">
      <div className="subhead"><h2>Channels</h2></div>
      <p className="muted" style={{ marginBottom: 12 }}>
        Where pauses and <code>send:</code> steps go, and where replies come from. Each is an adapter command in
        registries.json <code>channels</code>; the default is <code>channels.default</code> in config.yaml.
        A reply resolves a pause only from a sender on that channel's allow list.
      </p>
      {doc.channels.length === 0 && <p className="muted">No channels registered.</p>}
      {doc.channels.map(c => (
        <div key={c.name} style={{ marginBottom: 6 }}>
          <span className="mono" style={{ fontWeight: 550 }}>{c.name}</span>
          {c.default && <span className="pill" style={{ marginLeft: 6 }}>default</span>}
          <span className="muted"> · {c.inbound ? `send + receive every ${c.receiveEvery || '1m'}` : 'send only'} · {c.allow.length} allowed to answer</span>
          {c.lastError && <div className="danger-text" style={{ fontSize: 12 }}>last receive failed: {c.lastError}</div>}
        </div>))}
    </div>
    {doc.channels.some(c => c.inbound) && <ChannelMessages title="Received" />}
  </>)
}
