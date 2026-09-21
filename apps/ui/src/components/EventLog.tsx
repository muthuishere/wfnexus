import { useEffect, useRef } from 'react'
import type { Event } from '../api'

const short = (s: string, n = 400) => (s && s.length > n ? s.slice(0, n) + ' …' : s)

/** `task` is toolnexus' delegation built-in: a call to it means a sub-agent ran. */
const isDelegation = (e: Event) => (e.kind === 'tool_call' || e.kind === 'tool_result') && e.payload?.name === 'task'

const agentOf = (p: any): string =>
  p?.args?.agent || p?.args?.subagent_type || p?.args?.id || p?.agent || 'sub-agent'

function line(e: Event) {
  const p = e.payload || {}
  if (isDelegation(e)) {
    return e.kind === 'tool_call'
      ? <><span className="tag">delegate →</span> <b>{agentOf(p)}</b><pre>{short(p.args?.prompt || JSON.stringify(p.args), 800)}</pre></>
      : <><span className="tag">← {agentOf(p)} returned</span>{p.isError ? <span style={{ color: 'var(--err)' }}> error</span> : ''}<pre>{short(p.output, 1500)}</pre></>
  }
  switch (e.kind) {
    case 'tool_call': return <><b>{p.name}</b> <span className="muted">{short(JSON.stringify(p.args), 300)}</span></>
    case 'tool_result': return <><b>{p.name}</b> →{p.isError ? <span style={{ color: 'var(--err)' }}> error</span> : ''}<pre>{short(p.output, 1500)}</pre></>
    case 'llm': return p.text ? <><span className="muted">turn {p.turn}</span><pre>{short(p.text, 1500)}</pre></> : <span className="muted">turn {p.turn} · tool calls</span>
    case 'run.status': return <>run → <span className={`badge ${p.status}`}>{p.status}</span> {p.error && <span style={{ color: 'var(--err)' }}>{p.error}</span>}</>
    case 'step.status': return <>step <b>{e.stepId}</b> → <span className={`badge ${p.status}`}>{p.status}</span> {p.error && <span style={{ color: 'var(--err)' }}>{p.error}</span>}</>
    case 'artifact': return <>artifact <b>{p.name}</b> <span className="muted">{p.sizeBytes} bytes</span></>
    case 'decision': return <><span className="tag">judge</span> <span className="muted">{short(JSON.stringify(p.answers ?? p), 600)}</span></>
    case 'metric': return <span className="muted">{p.Event} {p.Tool || p.Model} {p.Ms}ms</span>
    default: return <span>{short(p.text || JSON.stringify(p), 600)}</span>
  }
}

export default function EventLog({ events, filter }: { events: Event[]; filter?: string }) {
  const end = useRef<HTMLDivElement>(null)
  const shown = events.filter(e => (!filter || e.stepId === filter) && e.kind !== 'metric')
  useEffect(() => { end.current?.scrollIntoView({ block: 'nearest' }) }, [shown.length])
  return (
    <div className="log">
      {shown.length === 0 && <div className="muted">No activity yet.</div>}
      {shown.map(e => (
        <div key={e.id} className={`ev ${e.kind}${e.payload?.isError ? ' err' : ''}${isDelegation(e) ? ' delegation' : ''}`}>
          <span className="k">{new Date(e.createdAt).toLocaleTimeString()} {e.stepId && `[${e.stepId}]`}</span>
          {line(e)}
        </div>
      ))}
      <div ref={end} />
    </div>
  )
}
