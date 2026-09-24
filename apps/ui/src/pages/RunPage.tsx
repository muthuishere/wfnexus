import { useCallback, useEffect, useMemo, useState } from 'react'
import { api, type Event, type RunDetail, type Step } from '../api'
import EventLog from '../components/EventLog'
import DecisionPanel from '../components/DecisionPanel'
import ApprovalBanner from '../components/ApprovalBanner'
import NeedsInputForm from '../components/NeedsInputForm'
import OutputContract from '../components/OutputContract'
import WorkflowCanvas from '../components/WorkflowCanvas'
import RunTimeline, { Cost, dur, money, tokens, type Usage } from '../components/RunTimeline'

// ── the run page ─────────────────────────────────────────────────────────────
// A run is WORK HAPPENING IN TIME. The page answers two questions in three
// seconds — what is happening, and what is it costing — and everything else is
// below that fold.
//
// HOW A RUNNING STEP REPORTS PROGRESS: stream the narrative, POLL the totals.
// The SSE stream carries what happened (llm, tool_call, tool_result, status)
// and replays its backlog, so the log is complete after a reload. It does NOT
// carry the step's aggregate: `turns`, `costUsd` and `costUnknown` live on the
// step ROW, written by the engine's usage accumulator on a bounded 1s cadence
// (engine/usage.go). Cost in particular cannot be recomputed from the stream —
// the per-million price is registry-side and never reaches the browser — so the
// aggregate has to be read back. This polls `GET /api/runs/{id}` every second
// while the run is live, matching that flush cadence exactly: faster would read
// the same row twice, slower would be visibly behind the work.

const PAUSED = ['awaiting_approval', 'needs_input']

/** "approved by muthu@deemwar.com (via api)" — the engine's own audit line
 *  (engine.go), and the only place WHO resolved a pause is recorded. */
function resolution(events: Event[]): { verb: string; who: string } | undefined {
  for (let i = events.length - 1; i >= 0; i--) {
    const m = /^(approved|rejected|answered) by ([^(]+?)\s*\(via/.exec(String(events[i].payload?.text ?? ''))
    if (m) return { verb: m[1], who: m[2] }
  }
}

export default function RunPage({ id }: { id: string }) {
  const [d, setD] = useState<RunDetail>()
  const [events, setEvents] = useState<Event[]>([])
  const [sel, setSel] = useState('')
  const [err, setErr] = useState('')
  const [now, setNow] = useState(() => Date.now())

  const refresh = useCallback(() => api.run(id).then(setD).catch(e => setErr(e.message)), [id])
  useEffect(() => { refresh() }, [refresh])
  useEffect(() => {
    // SSE replays the backlog from 0, so the log is complete after a reload too.
    return api.events(id, 0, ev => {
      setEvents(prev => (prev.some(p => p.id === ev.id) ? prev : [...prev, ev]))
      if (['run.status', 'step.status', 'artifact', 'decision'].includes(ev.kind)) refresh()
    })
  }, [id, refresh])

  const live = !!d && ['running', 'queued', ...PAUSED].includes(d.run.status)
  useEffect(() => {
    if (!live) return
    const t = setInterval(() => { setNow(Date.now()); refresh() }, 1000)
    return () => clearInterval(t)
  }, [live, refresh])

  const run = d?.run
  // The definition is the source of the SHAPE — but it comes off disk, so a
  // run whose workflow has since been deleted or renamed arrives with
  // `definition: null`. The run itself is still in the database, so the
  // timeline is drawn from the step rows instead of showing an empty page.
  const stepDefs = useMemo<Step[]>(() => {
    const defs = d?.definition?.steps
    if (defs?.length) return defs
    return (d?.steps || [])
      .slice().sort((a, b) => a.position - b.position)
      .map(s => ({ id: s.stepId, name: s.stepId, skills: [], tools: [], outputSchema: {} }))
  }, [d])
  const byId = useMemo(() => Object.fromEntries((d?.steps || []).map(s => [s.stepId, s])), [d])
  const act = (fn: Promise<unknown>) => fn.then(() => { setErr(''); refresh() }).catch(e => setErr(e.message))

  // The run's money and time, added up from the steps. A step with no price
  // is COUNTED but not added — the total says so rather than absorbing it.
  const totals = useMemo(() => {
    let usd = 0, tok = 0, unpriced = 0, priced = 0
    let first = Infinity, last = 0
    for (const s of d?.steps || []) {
      const u = s.usage as Usage | undefined
      tok += u?.totalTokens ?? 0
      if (s.startedAt) first = Math.min(first, Date.parse(s.startedAt))
      last = Math.max(last, s.finishedAt ? Date.parse(s.finishedAt) : (s.startedAt ? now : 0))
      if (typeof u?.costUsd === 'number') { usd += u.costUsd; priced++ }
      else if (u?.costUnknown) unpriced++
    }
    // Wall clock, not the sum of the steps: steps can run in parallel (waves),
    // and a sum would claim a run took longer than it did.
    return { usd, tok, unpriced, priced, ms: first === Infinity ? 0 : last - first }
  }, [d, now])

  if (!run) return <div className="muted">{err || 'loading…'}</div>

  const current = sel || run.currentStep || stepDefs[0]?.id || ''
  const curDef = stepDefs.find(s => s.id === current)
  const curRun = byId[current]
  const halted = byId[run.currentStep]
  const haltedDef = stepDefs.find(s => s.id === run.currentStep)
  const finalUrl = Object.values(byId).find(s => s.output?.pr_url)?.output?.pr_url as string | undefined
  const resolved = PAUSED.includes(run.status) ? undefined : resolution(events)
  const pausedSince = events.find(e =>
    e.kind === 'step.status' && e.stepId === run.currentStep && PAUSED.includes(e.payload?.status))?.createdAt
    || run.updatedAt

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

      {/* A PAUSE IS AN ACTION — it goes above everything, with the control in it. */}
      {run.status === 'awaiting_approval' && (
        <>
          <div className="waiting">waiting for you · {dur(now - Date.parse(pausedSince))}</div>
          <ApprovalBanner
            step={haltedDef} stepId={run.currentStep}
            onApprove={() => act(api.approve(run.id, run.currentStep))}
            onReject={reason => act(api.reject(run.id, run.currentStep, reason))} />
        </>)}

      {run.status === 'needs_input' && (
        <>
          <div className="waiting">waiting for you · {dur(now - Date.parse(pausedSince))}</div>
          <NeedsInputForm
            stepId={run.currentStep} reason={run.error} stepRun={halted}
            onSubmit={text => act(api.input(run.id, {
              extra_context: [run.input?.extra_context, text].filter(Boolean).join('\n\n'),
            }))} />
        </>)}

      {resolved && (
        <div className="banner ok"><b>{resolved.verb}</b> by <span className="mono">{resolved.who}</span></div>)}

      {run.status === 'failed' && (
        <div className="banner err"><b>Run failed</b> at <span className="mono">{run.currentStep}</span>: {run.error}
          <div className="actions"><button onClick={() => act(api.retry(run.id, run.currentStep))}>Retry step</button></div>
        </div>)}

      {run.status === 'done' && finalUrl && (
        <div className="banner ok"><b>PR published</b> — <a href={finalUrl} target="_blank" rel="noreferrer">{finalUrl}</a></div>)}

      {/* THE HERO: the run drawn as time, with the money beside the work. */}
      <div className="card runcard">
        <div className="runtop">
          <div className="big">
            {/* The known money is always shown as money — a run that is partly
                unpriced still has a real, spent number in it, and a run of
                local steps is genuinely free. What is missing is said beside
                it, never folded into it. */}
            <Cost usage={totals.priced ? { costUsd: totals.usd } : totals.unpriced ? { costUnknown: true } : undefined} big />
            {totals.unpriced > 0 && totals.priced > 0 &&
              <span className="muted"> + {totals.unpriced} unpriced step{totals.unpriced > 1 ? 's' : ''}</span>}
          </div>
          <div className="runfacts">
            <span>{dur(totals.ms)}</span>
            <span>{tokens(totals.tok)} tokens</span>
            <span>{stepDefs.length} step{stepDefs.length === 1 ? '' : 's'}</span>
          </div>
        </div>
        <RunTimeline steps={stepDefs} byId={byId} events={events} now={now} selected={current} onPick={setSel} />
      </div>

      <div className="grid cols-2">
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
              <b>model</b><span className="mono">{curDef?.model || curDef?.provider || 'default'}</span>
              <b>cost</b><span><Cost usage={curRun?.usage as Usage | undefined} /></span>
              <b>tokens</b><span className="mono">
                {(curRun?.usage as Usage)?.promptTokens !== undefined
                  ? `${tokens((curRun!.usage as Usage).promptTokens)} in · ${tokens((curRun!.usage as Usage).completionTokens)} out`
                  : tokens((curRun?.usage as Usage)?.totalTokens)}
              </span>
            </div>
            {curRun?.error && <div className="banner err" style={{ marginTop: 10 }}>{curRun.error}</div>}
            {curRun?.output && <><h3 style={{ marginTop: 14 }}>Validated output</h3><pre>{JSON.stringify(curRun.output, null, 2)}</pre></>}
            <h3 style={{ marginTop: 14 }}>Output contract</h3>
            <OutputContract schema={curDef?.outputSchema} />
          </div>

          <DecisionPanel decision={curRun?.decision} step={curDef} />

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
            <details>
              <summary><b>Input &amp; shape</b></summary>
              <pre style={{ marginTop: 10 }}>{JSON.stringify(run.input, null, 2)}</pre>
              {stepDefs.length > 1 && (
                <div style={{ marginTop: 10 }}>
                  <WorkflowCanvas steps={stepDefs} active={current} onPick={setSel} />
                </div>)}
            </details>
          </div>
        </div>

        <div>
          <div className="card">
            <div style={{ display: 'flex', alignItems: 'center' }}><h3>Activity · {current || 'all'}</h3>
              <button className="ghost" style={{ marginLeft: 'auto' }} onClick={() => setSel('')}>All steps</button></div>
            <EventLog events={events} filter={sel || undefined} />
          </div>
        </div>
      </div>
      <div className="muted" style={{ fontSize: 11.5, marginTop: 10 }}>
        {live ? 'live · totals refresh every second' : `finished ${new Date(run.updatedAt).toLocaleString()}`}
        {totals.unpriced > 0 && ` · ${totals.unpriced} step(s) ran on a provider with no price in the registry — shown as unknown, never as ${money(0)}`}
      </div>
    </>
  )
}
