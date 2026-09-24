import { useCallback, useEffect, useMemo, useState } from 'react'
import WorkflowCanvas from '../components/WorkflowCanvas'
import { api, type Event, type RunDetail } from '../api'
import EventLog from '../components/EventLog'
import DecisionPanel from '../components/DecisionPanel'
import ApprovalBanner from '../components/ApprovalBanner'
import NeedsInputForm from '../components/NeedsInputForm'
import OutputContract from '../components/OutputContract'

export default function RunPage({ id }: { id: string }) {
  const [d, setD] = useState<RunDetail>()
  const [events, setEvents] = useState<Event[]>([])
  const [sel, setSel] = useState<string>('')
  const [err, setErr] = useState('')

  const refresh = useCallback(() => api.run(id).then(setD).catch(e => setErr(e.message)), [id])
  useEffect(() => { refresh() }, [refresh])
  useEffect(() => {
    // SSE replays the backlog from 0, so the log is complete after a reload too.
    return api.events(id, 0, ev => {
      setEvents(prev => (prev.some(p => p.id === ev.id) ? prev : [...prev, ev]))
      if (['run.status', 'step.status', 'artifact', 'decision'].includes(ev.kind)) refresh()
    })
  }, [id, refresh])

  const run = d?.run
  const stepDefs = useMemo(() => d?.definition?.steps || [], [d])
  const byId = useMemo(() => Object.fromEntries((d?.steps || []).map(s => [s.stepId, s])), [d])
  const current = sel || run?.currentStep || stepDefs[0]?.id || ''
  const curDef = stepDefs.find(s => s.id === current)
  const curRun = byId[current]
  const act = (fn: Promise<unknown>) => fn.then(() => { setErr(''); refresh() }).catch(e => setErr(e.message))

  if (!run) return <div className="muted">{err || 'loading…'}</div>
  const halted = byId[run.currentStep]
  const haltedDef = stepDefs.find(s => s.id === run.currentStep)
  const finalUrl = Object.values(byId).find(s => s.output?.pr_url)?.output?.pr_url as string | undefined

  return (
    <>
      <div className="head" style={{ alignItems: 'center' }}>
        <div style={{ minWidth: 0 }}>
          <div className="muted" style={{ fontSize: 12.5, marginBottom: 2 }}>
            <a href="#/projects">Projects</a>
            {' / '}<a href={`#/projects/${encodeURIComponent(run.project || 'local')}`} className="mono">{run.project || 'local'}</a>
            {' / '}<a href={`#/projects/${encodeURIComponent(run.project || 'local')}/${encodeURIComponent(run.workflow)}`} className="mono">{run.workflow}</a>
          </div>
          <h1>{run.input?.title || run.workflow}</h1>
          <div className="muted mono" style={{ fontSize: 12, marginTop: 3 }}>{run.id}</div>
        </div>
        <span className={`badge ${run.status}`}>{run.status.replace(/_/g, ' ')}</span>
        {['running', 'queued'].includes(run.status) && <button className="ghost" onClick={() => act(api.cancel(run.id))}>Cancel</button>}
      </div>
      {err && <div className="banner err">{err}</div>}

      {run.status === 'awaiting_approval' && (
        <ApprovalBanner
          step={haltedDef} stepId={run.currentStep}
          onApprove={() => act(api.approve(run.id, run.currentStep))}
          onReject={reason => act(api.reject(run.id, run.currentStep, reason))} />)}

      {run.status === 'needs_input' && (
        <NeedsInputForm
          stepId={run.currentStep} reason={run.error} stepRun={halted}
          onSubmit={text => act(api.input(run.id, {
            extra_context: [run.input?.extra_context, text].filter(Boolean).join('\n\n'),
          }))} />)}

      {run.status === 'failed' && (
        <div className="banner err"><b>Run failed</b> at <span className="mono">{run.currentStep}</span>: {run.error}
          <div className="actions"><button onClick={() => act(api.retry(run.id, run.currentStep))}>Retry step</button></div>
        </div>)}

      {run.status === 'done' && finalUrl && (
        <div className="banner ok"><b>PR published</b> — <a href={finalUrl} target="_blank" rel="noreferrer">{finalUrl}</a></div>)}

      {stepDefs.length > 1 && (
        <div className="card">
          <div className="subhead"><h2>Shape</h2></div>
          <div className="muted" style={{ fontSize: 12, marginBottom: 8 }}>
            Which step this run is on, in the order the engine derived. For a goal-planned
            workflow the routes not taken are visible here and nowhere else.
          </div>
          <WorkflowCanvas steps={stepDefs} active={current} onPick={setSel} />
        </div>)}

      <div className="grid cols-2">
        <div>
          <div className="card">
            <div className="subhead"><h2>Steps</h2></div>
            <div className="steps">
              {stepDefs.map((s, i) => {
                const st = byId[s.id]
                return (
                  <div key={s.id} className={`step ${current === s.id ? 'active' : ''}`} onClick={() => setSel(s.id)}>
                    <div className={`dot ${st?.status || 'pending'}`} />
                    <div style={{ flex: 1, minWidth: 0 }}>
                      <div><b>{i + 1}. {s.name}</b> {s.requiresApproval && <span className="badge awaiting_approval">gate</span>}</div>
                      <div className="muted" style={{ fontSize: 12 }}>
                        <span className={`badge ${st?.status || 'pending'}`}>{st?.status || 'pending'}</span>
                        {!!st?.turns && <> · {st.turns} turns · {st.attempts} attempt{st.attempts > 1 ? 's' : ''}</>}
                        {st?.decision && <> · <span className="badge running">judged</span></>}
                      </div>
                      <div className="chips">{(s.skills ?? []).map(x => <span key={x}>{x}</span>)}</div>
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
            <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
              <h2 style={{ margin: 0 }}>{curDef?.name || current}</h2>
              <span className={`badge ${curRun?.status || 'pending'}`}>{curRun?.status || 'pending'}</span>
              {curRun && curRun.status !== 'running' && <button className="ghost" style={{ marginLeft: 'auto' }} onClick={() => act(api.retry(run.id, current))}>Re-run from here</button>}
            </div>
            <div className="kv" style={{ marginTop: 10 }}>
              <b>skills</b><span className="mono">{curDef?.skills?.join(', ') || '—'}</span>
              <b>tools</b><span className="mono">{curDef?.tools?.join(', ') || '—'}</span>
              <b>mcp</b><span className="mono">{curDef?.mcp?.join(', ') || '—'}</span>
              <b>team</b><span className="mono">{curDef?.team?.map(m => m.id).join(', ') || '—'}</span>
              <b>guardrails</b><span className="mono">{curDef?.guardrails?.length ? `${curDef.guardrails.length} deny rules` : '—'}</span>
              <b>model</b><span className="mono">{curDef?.model || 'default'}</span>
              <b>usage</b><span className="mono">{curRun?.usage ? JSON.stringify(curRun.usage) : '—'}</span>
            </div>
            {curRun?.error && <div className="banner err" style={{ marginTop: 10 }}>{curRun.error}</div>}
            {curRun?.output && <><h3 style={{ marginTop: 14 }}>Validated output</h3><pre>{JSON.stringify(curRun.output, null, 2)}</pre></>}
            <h3 style={{ marginTop: 14 }}>Output contract</h3>
            <OutputContract schema={curDef?.outputSchema} />
          </div>

          <DecisionPanel decision={curRun?.decision} step={curDef} />

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
