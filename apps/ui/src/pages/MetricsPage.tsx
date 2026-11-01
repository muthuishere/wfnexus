import { useEffect, useState } from 'react'
import { api, type Counts, type Metrics, type MetricsWindow } from '../api'

/** MetricsPage is the server's own operational picture: how runs went, how long
 *  steps take, who is being kept waiting, what the models cost, and whether the
 *  machines are there. It reads one aggregate from /api/metrics — no Prometheus
 *  or Grafana needed (the same numbers are at /metrics for anyone who has one).
 *
 *  A system page, like System and Workers: it sums over every project and
 *  links back into the drill-down rather than listing runs itself. */
export default function MetricsPage() {
  const [win, setWin] = useState<MetricsWindow>(() => {
    try { return (localStorage.getItem('wfx.metrics.window') as MetricsWindow) || '24h' } catch { return '24h' }
  })
  const [m, setM] = useState<Metrics>()
  const [err, setErr] = useState('')
  const [tick, setTick] = useState(0)

  useEffect(() => {
    let live = true
    api.metrics(win).then(r => { if (live) { setM(r); setErr('') } }).catch(e => { if (live) setErr(e instanceof Error ? e.message : String(e)) })
    try { localStorage.setItem('wfx.metrics.window', win) } catch { /* private mode */ }
    return () => { live = false }
  }, [win, tick])

  return (
    <>
      <div className="head">
        <div>
          <h1>Metrics</h1>
          <p className="muted">
            Every project on this server, summed over the window. Waiting, queued and workers are right now.
          </p>
        </div>
        <div className="mx-controls">
          <div className="mx-range" role="group" aria-label="Time range">
            {(['24h', '7d', '30d'] as MetricsWindow[]).map(w => (
              <button key={w} className={w === win ? 'active' : ''} aria-pressed={w === win} onClick={() => setWin(w)}>{w}</button>
            ))}
          </div>
          <button className="ghost" onClick={() => setTick(t => t + 1)}>Refresh</button>
        </div>
      </div>
      {err && <div className="banner err">{err}</div>}
      {!m ? <div className="muted">loading…</div> : <Body m={m} />}
    </>
  )
}

// Run status → the status token it wears. Status colour is reserved for state
// and always sits beside its label.
const STATUS: Array<[string, string, string]> = [
  ['done', 'var(--ok)', 'done'],
  ['failed', 'var(--err)', 'failed'],
  ['cancelled', 'var(--muted)', 'cancelled'],
  ['awaiting_approval', 'var(--warn)', 'waiting'],
  ['needs_input', 'var(--warn)', 'needs input'],
  ['running', 'var(--accent-500)', 'running'],
  ['queued', 'var(--border-strong)', 'queued'],
]

function Body({ m }: { m: Metrics }) {
  const finished = (m.runs.byStatus.done || 0) + (m.runs.byStatus.failed || 0)
  const success = finished ? (m.runs.byStatus.done || 0) / finished : 0
  const workflows = m.runs.byWorkflow || []
  const slowest = m.steps.slowest || []
  const models = m.llm.models || []
  const tokens = m.llm.promptTokens + m.llm.completionTokens
  return (
    <>
      <div className="mx-tiles">
        <Tile label="Runs" value={fmtInt(m.runs.total)} sub={finished ? `${pctS(success)} succeeded` : 'none finished'} />
        <Tile label="Step failure rate" value={pctS(m.steps.failureRate)} sub={`${fmtInt(m.steps.failed)} of ${fmtInt(m.steps.finished)} steps`} tone={m.steps.failureRate > .1 ? 'err' : undefined} />
        <Tile label="Retry rate" value={pctS(m.steps.retryRate)} sub={`${fmtInt(m.steps.retried)} steps retried`} />
        <Tile label="Waiting on a person" value={fmtInt(m.approvals.waiting)}
          sub={m.approvals.oldestWaiting ? `oldest ${ago(m.approvals.oldestWaiting)}` : 'nobody waiting'} tone={m.approvals.waiting ? 'warn' : undefined} />
        <Tile label="LLM spend" value={usd(m.llm.costUsd)} sub={`${fmtTokens(tokens)} tokens${m.llm.unpricedSteps ? ` · ${m.llm.unpricedSteps} unpriced steps` : ''}`} />
        <Tile label="Queue" value={fmtInt(m.queue.runsQueued + m.queue.jobsQueued)}
          sub={`${m.queue.runsRunning} running${m.queue.oldestQueued ? ` · oldest ${ago(m.queue.oldestQueued)}` : ''}`} />
      </div>

      <section className="card">
        <div className="mx-cardhead">
          <h2>Runs over time</h2>
          <Legend keys={STATUS.filter(([k]) => m.timeline.some(b => b.byStatus[k]))} />
        </div>
        <Timeline m={m} />
      </section>

      <div className="mx-grid">
        <section className="card">
          <h2>Runs by workflow</h2>
          {!workflows.length ? <p className="muted mx-empty">No runs in this window.</p> : (
            <table className="mx-table">
              <thead><tr><th>Workflow</th><th className="r">Runs</th><th style={{ width: '34%' }}>Outcome</th><th className="r">p50</th><th className="r">p95</th></tr></thead>
              <tbody>
                {workflows.slice(0, 12).map(w => (
                  <tr key={w.project + '/' + w.workflow}>
                    <td><a href={`#/projects/${encodeURIComponent(w.project)}/${encodeURIComponent(w.workflow)}`}>{w.workflow}</a>
                      <div className="muted mx-small">{w.project}</div></td>
                    <td className="r">{fmtInt(w.total)}</td>
                    <td><StackBar counts={w.byStatus} total={w.total} /></td>
                    <td className="r">{w.durations ? dur(w.p50Ms) : '—'}</td>
                    <td className="r">{w.durations ? dur(w.p95Ms) : '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </section>

        <section className="card">
          <h2>Slowest steps <span className="muted mx-small">by p95</span></h2>
          {!slowest.length ? <p className="muted mx-empty">No finished steps in this window.</p> : (
            <table className="mx-table">
              <thead><tr><th>Step</th><th className="r">n</th><th style={{ width: '38%' }}>p50 · p95</th><th className="r">p95</th></tr></thead>
              <tbody>
                {slowest.slice(0, 10).map(s => (
                  <tr key={s.project + s.workflow + s.step}>
                    <td><span className="mono">{s.step}</span><div className="muted mx-small">{s.workflow}</div></td>
                    <td className="r">{s.count}</td>
                    <td><RangeBar p50={s.p50Ms} p95={s.p95Ms} max={slowest[0].p95Ms} /></td>
                    <td className="r">{dur(s.p95Ms)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </section>
      </div>

      <section className="card">
        <div className="mx-cardhead">
          <h2>Models</h2>
          <span className="muted mx-small">
            {fmtInt(m.llm.calls)} calls · {fmtInt(m.llm.errors)} errors · {fmtInt(m.llm.fallbacks)} provider fallbacks · {fmtInt(m.steps.toolCalls)} tool calls ({fmtInt(m.steps.toolErrors)} failed)
          </span>
        </div>
        {!models.length ? <p className="muted mx-empty">No model calls in this window.</p> : (
          <table className="mx-table">
            <thead><tr><th>Provider</th><th>Model</th><th className="r">Calls</th><th className="r">Errors</th><th className="r">Prompt</th><th className="r">Completion</th><th style={{ width: '22%' }}>Share of tokens</th><th className="r">Cost</th></tr></thead>
            <tbody>
              {models.map(x => {
                const t = x.promptTokens + x.completionTokens
                return (
                  <tr key={x.provider + x.model}>
                    <td>{x.provider}</td>
                    <td className="mono">{x.model}</td>
                    <td className="r">{fmtInt(x.calls)}</td>
                    <td className={'r' + (x.errors ? ' mx-bad' : '')}>{fmtInt(x.errors)}</td>
                    <td className="r">{fmtTokens(x.promptTokens)}</td>
                    <td className="r">{fmtTokens(x.completionTokens)}</td>
                    <td><Bar frac={tokens ? t / tokens : 0} title={`${fmtInt(t)} tokens`} /></td>
                    <td className="r">{x.costKnown ? usd(x.costUsd) : <span className="muted" title="no price known for this model">unknown</span>}</td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        )}
      </section>

      <div className="mx-grid">
        <section className="card">
          <h2>Approvals</h2>
          <dl className="mx-dl">
            <div><dt>Waiting now</dt><dd>{fmtInt(m.approvals.waiting)}</dd></div>
            <div><dt>Resolved in window</dt><dd>{fmtInt(m.approvals.resolved)}</dd></div>
            <div><dt>Time to resolve, p50</dt><dd>{m.approvals.resolved ? dur(m.approvals.p50Ms) : '—'}</dd></div>
            <div><dt>Time to resolve, p95</dt><dd>{m.approvals.resolved ? dur(m.approvals.p95Ms) : '—'}</dd></div>
          </dl>
          {!!Object.keys(m.approvals.byResolution).length && (
            <p className="muted mx-small" style={{ marginTop: 10 }}>
              {Object.entries(m.approvals.byResolution).map(([k, v]) => `${v} ${k}`).join(' · ')}
            </p>
          )}
          {m.approvals.waiting > 0 && <p style={{ marginTop: 10 }}>The <strong>waiting on you</strong> button at the top lists them.</p>}
        </section>

        <section className="card">
          <div className="mx-cardhead">
            <h2>Workers</h2>
            <span className="muted mx-small">{m.workers.online} of {m.workers.total} online · {m.queue.jobsQueued} jobs queued · {m.queue.jobsLeased} leased</span>
          </div>
          {!m.workers.workers.length ? <p className="muted mx-empty">No machines have joined; this server runs every step itself. <a href="#/workers">Add one</a>.</p> : (
            <table className="mx-table">
              <thead><tr><th>Machine</th><th>Status</th><th>Labels</th><th className="r">Last seen</th></tr></thead>
              <tbody>
                {m.workers.workers.map(w => (
                  <tr key={w.name}>
                    <td className="mono">{w.name}</td>
                    <td><span className={`badge ${w.status === 'online' ? 'done' : 'failed'}`}>{w.status}</span></td>
                    <td className="muted">{w.labels.join(', ')}</td>
                    <td className="r">{ago(w.lastSeen)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </section>
      </div>
      <p className="muted mx-small" style={{ marginTop: 16 }}>
        Since {new Date(m.since).toLocaleString()} · Prometheus text format at <code>/metrics</code>
      </p>
    </>
  )
}

function Tile({ label, value, sub, tone }: { label: string; value: string; sub?: string; tone?: 'err' | 'warn' }) {
  return (
    <div className={'mx-tile' + (tone ? ` ${tone}` : '')}>
      <div className="mx-label">{label}</div>
      <div className="mx-value">{value}</div>
      {sub && <div className="mx-sub">{sub}</div>}
    </div>
  )
}

function Legend({ keys }: { keys: Array<[string, string, string]> }) {
  return (
    <div className="mx-legend">
      {keys.map(([k, c, label]) => <span key={k}><i style={{ background: c }} />{label}</span>)}
    </div>
  )
}

/** Stacked columns, one per bucket; empty buckets are present so the x-axis is time. */
function Timeline({ m }: { m: Metrics }) {
  const [hover, setHover] = useState<number>()
  const max = Math.max(1, ...m.timeline.map(b => sum(b.byStatus)))
  const H = 140
  const n = m.timeline.length
  const hourly = m.bucketSeconds < 86400
  const label = (s: string) => {
    const d = new Date(s)
    return hourly ? d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : d.toLocaleDateString([], { month: 'short', day: 'numeric' })
  }
  const step = Math.ceil(n / (hourly ? 8 : 10))
  const hb = hover !== undefined ? m.timeline[hover] : undefined
  return (
    <div className="mx-timeline">
      <div className="mx-ymax">{max}</div>
      <svg viewBox={`0 0 ${n * 10} ${H}`} preserveAspectRatio="none" role="img" aria-label="Runs per bucket by status"
        onMouseLeave={() => setHover(undefined)}>
        <line x1={0} x2={n * 10} y1={H - .5} y2={H - .5} stroke="var(--border)" strokeWidth={1} vectorEffect="non-scaling-stroke" />
        {m.timeline.map((b, i) => {
          let y = H
          return (
            <g key={i} onMouseEnter={() => setHover(i)}>
              <rect x={i * 10} y={0} width={10} height={H} fill={hover === i ? 'var(--subtle)' : 'transparent'} />
              {STATUS.map(([k, c]) => {
                const v = b.byStatus[k] || 0
                if (!v) return null
                const h = (v / max) * (H - 6)
                y -= h
                // 1px surface gap between stacked segments
                return <rect key={k} x={i * 10 + 1.5} y={y + .5} width={7} height={Math.max(h - 1, .5)} fill={c} />
              })}
            </g>
          )
        })}
      </svg>
      <div className="mx-xaxis">
        {m.timeline.map((b, i) => <span key={i} style={{ left: `${((i + .5) / n) * 100}%` }}>{i % step === 0 ? label(b.start) : ''}</span>)}
      </div>
      <div className="mx-tip" aria-live="polite">
        {hb ? <>
          <strong>{hourly ? new Date(hb.start).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : label(hb.start)}</strong>
          {' · '}{sum(hb.byStatus)} runs
          {STATUS.filter(([k]) => hb.byStatus[k]).map(([k, , l]) => <span key={k}> · {hb.byStatus[k]} {l}</span>)}
        </> : <span className="muted">Hover a column for its numbers.</span>}
      </div>
    </div>
  )
}

function StackBar({ counts, total }: { counts: Counts; total: number }) {
  const title = STATUS.filter(([k]) => counts[k]).map(([k, , l]) => `${counts[k]} ${l}`).join(' · ')
  return (
    <div className="mx-stack" title={title} aria-label={title}>
      {STATUS.map(([k, c]) => counts[k] ? <i key={k} style={{ width: `${(counts[k] / total) * 100}%`, background: c }} /> : null)}
    </div>
  )
}

function Bar({ frac, title }: { frac: number; title?: string }) {
  return <div className="mx-bar" title={title}><i style={{ width: `${Math.max(frac * 100, frac > 0 ? 1 : 0)}%` }} /></div>
}

/** p50 as the solid part, p50→p95 as the lighter tail, on a shared scale. */
function RangeBar({ p50, p95, max }: { p50: number; p95: number; max: number }) {
  const s = (v: number) => `${max ? (v / max) * 100 : 0}%`
  return (
    <div className="mx-bar" title={`p50 ${dur(p50)} · p95 ${dur(p95)}`}>
      <i className="tail" style={{ width: s(p95) }} />
      <i style={{ width: s(p50) }} />
    </div>
  )
}

const sum = (c: Counts) => Object.values(c).reduce((a, b) => a + b, 0)
const fmtInt = (n: number) => n.toLocaleString()
const pctS = (f: number) => `${(f * 100).toFixed(f && f < .1 ? 1 : 0)}%`
const usd = (n: number) => n === 0 ? '$0' : n < 0.01 ? `$${n.toFixed(4)}` : `$${n.toFixed(2)}`
function fmtTokens(n: number) {
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}k`
  return String(n)
}
function dur(ms: number) {
  if (ms < 1000) return `${ms}ms`
  const s = ms / 1000
  if (s < 60) return `${s.toFixed(s < 10 ? 1 : 0)}s`
  const m = s / 60
  if (m < 60) return `${Math.floor(m)}m ${Math.round(s % 60)}s`
  const h = m / 60
  return h < 48 ? `${Math.floor(h)}h ${Math.round(m % 60)}m` : `${Math.round(h / 24)}d`
}
function ago(iso: string) {
  const ms = Date.now() - new Date(iso).getTime()
  return ms < 60_000 ? 'just now' : `${dur(ms).split(' ')[0]} ago`
}
