import { useCallback, useEffect, useMemo, useState } from 'react'
import { api, type Event, type RunDetail } from '../api'
import EventLog from '../components/EventLog'

export default function RunPage({ id }: { id: string }) {
  const [d, setD] = useState<RunDetail>()
  const [events, setEvents] = useState<Event[]>([])
  const [sel, setSel] = useState<string>('')
  const [answers, setAnswers] = useState<Record<string, string>>({})
  const [err, setErr] = useState('')

  const refresh = useCallback(() => api.run(id).then(setD).catch(e => setErr(e.message)), [id])
  useEffect(() => { refresh() }, [refresh])
  useEffect(() => {
    // SSE replays the backlog from 0, so the log is complete after a reload too.
    return api.events(id, 0, ev => {
      setEvents(prev => (prev.some(p => p.id === ev.id) ? prev : [...prev, ev]))
      if (ev.kind === 'run.status' || ev.kind === 'step.status' || ev.kind === 'artifact') refresh()
    })
  }, [id, refresh])

  const run = d?.run
  const stepDefs = d?.definition?.steps || []
  const byId = useMemo(() => Object.fromEntries((d?.steps || []).map(s => [s.stepId, s])), [d])
  const current = sel || run?.currentStep || stepDefs[0]?.id || ''
  const curDef = stepDefs.find(s => s.id === current)
  const curRun = byId[current]
  const act = (fn: Promise<any>) => fn.then(() => { setErr(''); refresh() }).catch(e => setErr(e.message))

  if (!run) return <div className="muted">{err || 'loading…'}</div>
  const missing: string[] = run.status === 'needs_input' ? (byId[run.currentStep]?.output?.missing_info || []) : []

  return (
    <>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 14 }}>
        <div>
          <h1>{run.input?.title || run.workflow}</h1>
          <div className="muted mono">{run.workflow} · {run.id}</div>
        </div>
        <span className={`badge ${run.status}`} style={{ marginLeft: 'auto' }}>{run.status.replace('_', ' ')}</span>
        {['running', 'queued'].includes(run.status) && <button className="ghost" onClick={() => act(api.cancel(run.id))}>Cancel</button>}
      </div>
      {err && <div className="banner err">{err}</div>}

      {run.status === 'awaiting_approval' && (
        <div className="banner warn">
          <b>Approval required</b> before step <span className="mono">{run.currentStep}</span> — it publishes outside this machine.
          <div className="actions">
            <button onClick={() => act(api.approve(run.id, run.currentStep))}>Approve &amp; publish</button>
            <button className="danger" onClick={() => act(api.reject(run.id, run.currentStep, prompt('Reason?') || 'rejected'))}>Reject</button>
          </div>
        </div>)}

      {run.status === 'needs_input' && (
        <div className="banner warn">
          <b>More information needed</b> — {run.error}
          {missing.map((q, i) => (<div key={i}><label>{q}</label><input value={answers[q] || ''} onChange={e => setAnswers({ ...answers, [q]: e.target.value })} /></div>))}
          {missing.length === 0 && <div><label>extra_context</label><textarea value={answers._ || ''} onChange={e => setAnswers({ _: e.target.value })} /></div>}
          <div className="actions">
            <button onClick={() => act(api.input(run.id, {
              extra_context: [run.input?.extra_context, ...Object.entries(answers).map(([q, a]) => (q === '_' ? a : `${q}\n→ ${a}`))].filter(Boolean).join('\n\n'),
            }))}>Submit &amp; continue</button>
          </div>
        </div>)}

      {run.status === 'failed' && <div className="banner err"><b>Run failed</b> at <span className="mono">{run.currentStep}</span>: {run.error}
        <div className="actions"><button onClick={() => act(api.retry(run.id, run.currentStep))}>Retry step</button></div></div>}

      {run.status === 'done' && byId['finalize-pr']?.output?.pr_url && (
        <div className="banner ok"><b>PR published</b> — <a href={byId['finalize-pr'].output.pr_url} target="_blank" rel="noreferrer">{byId['finalize-pr'].output.pr_url}</a></div>)}

      <div className="grid cols-2">
        <div>
          <div className="card">
            <h3>Steps</h3>
            <div className="steps">
              {stepDefs.map((s, i) => {
                const st = byId[s.id]
                return (
                  <div key={s.id} className={`step ${current === s.id ? 'active' : ''}`} onClick={() => setSel(s.id)}>
                    <div className={`dot ${st?.status || 'pending'}`} />
                    <div style={{ flex: 1 }}>
                      <div><b>{i + 1}. {s.name}</b> {s.requiresApproval && <span className="badge awaiting_approval">gate</span>}</div>
                      <div className="muted" style={{ fontSize: 12 }}>
                        <span className={`badge ${st?.status || 'pending'}`}>{st?.status || 'pending'}</span>
                        {!!st?.turns && <> · {st.turns} turns · {st.attempts} attempt{st.attempts > 1 ? 's' : ''}</>}
                      </div>
                      <div className="chips">{s.skills.map(x => <span key={x}>{x}</span>)}</div>
                    </div>
                  </div>)
              })}
            </div>
          </div>
          {d!.artifacts.length > 0 && (
            <div className="card">
              <h3>Artifacts</h3>
              {d!.artifacts.map(a => (
                <div key={a.id} style={{ marginBottom: 6 }}>
                  <a href={`/api/runs/${run.id}/artifacts/${a.id}?inline=1`} target="_blank" rel="noreferrer" className="mono">{a.stepId}/{a.name}</a>
                  <span className="muted"> {(a.sizeBytes / 1024).toFixed(1)} KB</span>
                </div>))}
            </div>)}
          <div className="card">
            <h3>Input</h3>
            <pre>{JSON.stringify(run.input, null, 2)}</pre>
          </div>
        </div>

        <div>
          <div className="card">
            <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
              <h2>{curDef?.name || current}</h2>
              <span className={`badge ${curRun?.status || 'pending'}`}>{curRun?.status || 'pending'}</span>
              {curRun && curRun.status !== 'running' && <button className="ghost" style={{ marginLeft: 'auto' }} onClick={() => act(api.retry(run.id, current))}>Re-run from here</button>}
            </div>
            <div className="kv" style={{ marginTop: 10 }}>
              <b>skills</b><span className="mono">{curDef?.skills.join(', ') || '—'}</span>
              <b>tools</b><span className="mono">{curDef?.tools.join(', ') || '—'}</span>
              <b>model</b><span className="mono">{curDef?.model || 'default'}</span>
              <b>usage</b><span className="mono">{curRun?.usage ? JSON.stringify(curRun.usage) : '—'}</span>
            </div>
            {curRun?.error && <div className="banner err" style={{ marginTop: 10 }}>{curRun.error}</div>}
            {curRun?.output && <><h3 style={{ marginTop: 14 }}>Validated output</h3><pre>{JSON.stringify(curRun.output, null, 2)}</pre></>}
            {curDef && <><h3 style={{ marginTop: 14 }}>Output contract</h3><pre>{JSON.stringify(curDef.outputSchema, null, 2)}</pre></>}
          </div>
          <div className="card">
            <div style={{ display: 'flex', alignItems: 'center' }}><h3>Activity · {current}</h3>
              <button className="ghost" style={{ marginLeft: 'auto' }} onClick={() => setSel('')}>All steps</button></div>
            <EventLog events={events} filter={sel || undefined} />
          </div>
        </div>
      </div>
    </>
  )
}
