import { useCallback, useEffect, useMemo, useState } from 'react'
import { api, type Event, type RunDetail, type Step, type StepRun } from '../api'
import EventLog from '../components/EventLog'
import DecisionPanel from '../components/DecisionPanel'
import ApprovalBanner from '../components/ApprovalBanner'
import NeedsInputForm from '../components/NeedsInputForm'
import OutputContract from '../components/OutputContract'
import WorkflowCanvas from '../components/WorkflowCanvas'
import RunTimeline, { Cost, dur, tokens, type Usage } from '../components/RunTimeline'
import Markdown from '../components/Markdown'
import { headlineOf, hhmm, isScript, summaryOf } from '../lib/story'

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

/** The most recent pause resolution on this run, read from the step ROWS —
 *  resolvedBy/resolvedAt/resolution are the engine's audit fact (ADR 0021).
 *  It used to be scraped from the "approved by …" log line, which only told
 *  approve/reject/answer apart by wording and lost the reason and the time. */
export function resolutionOf(steps: StepRun[]): StepRun | undefined {
  let last: StepRun | undefined
  for (const s of steps) {
    if (!s.resolvedBy || !s.resolvedAt) continue
    if (!last || Date.parse(s.resolvedAt) > Date.parse(last.resolvedAt!)) last = s
  }
  return last
}

/** One step's resolution, as the people reading an audit want it said. */
function Resolved({ s }: { s: StepRun }) {
  return (
    <>
      <b>{s.resolution || 'resolved'}</b> by <span className="mono">{s.resolvedBy}</span>
      {s.resolvedAt && <span className="muted"> · {new Date(s.resolvedAt).toLocaleString()}</span>}
      {s.resolutionReason && <span> — {s.resolutionReason}</span>}
    </>
  )
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
  // "next run at" — only worth asking once, for the workflow this run is of.
  const [nextRunAt, setNextRunAt] = useState<string | null>(null)
  const wfName = run?.workflow
  useEffect(() => {
    if (!wfName) return
    api.workflowSchedule(wfName).then(s => setNextRunAt(s.nextRunAt)).catch(() => setNextRunAt(null))
  }, [wfName])
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
  const resolved = PAUSED.includes(run.status) ? undefined : resolutionOf(d?.steps || [])
  const pausedSince = events.find(e =>
    e.kind === 'step.status' && e.stepId === run.currentStep && PAUSED.includes(e.payload?.status))?.createdAt
    || run.updatedAt

  const head = headlineOf(run)
  // What happens next: who it is waiting on, or when the schedule runs it again.
  const next = PAUSED.includes(run.status)
    ? `waiting on ${haltedDef?.approvers?.length ? haltedDef.approvers.join(', ') : 'you'} · ${dur(now - Date.parse(pausedSince))} so far`
    : live ? 'still working'
      : nextRunAt ? `next run at ${hhmm(nextRunAt)}` : run.status === 'failed' ? 'retry the failed step, or fix and run again' : ''
  const found = stepDefs.map(s => ({ def: s, md: summaryOf(byId[s.id]) })).filter(x => x.md)
  const script = isScript(curDef)
  const curSummary = summaryOf(curRun)
  const curUsage = curRun?.usage as Usage | undefined

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
        {['running', 'queued'].includes(run.status) && <button className="ghost" onClick={() => act(api.cancel(run.id))}>Cancel</button>}
      </div>
      {err && <div className="banner err">{err}</div>}

      {/* THE VERDICT: what this run found, in one sentence, and what is next. */}
      <div className={`card verdict ${head.verdict}`}>
        <div className="vline">
          <span className={`vword ${head.verdict}`}>{head.verdict === 'running' ? 'working' : head.verdict}</span>
          <span className="vtext">{head.text}</span>
        </div>
        <div className="vfacts">
          <span className={`badge ${run.status}`}>{run.status.replace(/_/g, ' ')}</span>
          <span>{dur(totals.ms)}</span>
          <span>{new Date(run.startedAt || run.createdAt).toLocaleString()}</span>
          {next && <span className="vnext">next: {next}</span>}
          {run.status === 'failed' && run.currentStep &&
            <button style={{ marginLeft: 'auto' }} onClick={() => act(api.retry(run.id, run.currentStep))}>Retry step</button>}
        </div>
        {run.status === 'failed' && run.error && head.text !== run.error.split('\n')[0] &&
          <div className="muted" style={{ marginTop: 8, fontSize: 12.5 }}>at <span className="mono">{run.currentStep}</span>: {run.error}</div>}
        {run.status === 'done' && finalUrl &&
          <div style={{ marginTop: 8 }}><b>PR published</b> — <a href={finalUrl} target="_blank" rel="noreferrer">{finalUrl}</a></div>}
      </div>

      {/* A PAUSE IS AN ACTION — the control right under the verdict. */}
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

      {resolved && (
        <div className="banner ok"><span className="mono">{resolved.stepId}</span>: <Resolved s={resolved} /></div>)}

      {found.length > 0 && (
        <div className="card">
          <h2>What it found</h2>
          {found.map(f => (
            <div key={f.def.id} className="found" onClick={() => setSel(f.def.id)}>
              {found.length > 1 && <div className="muted mono found-step">{f.def.name || f.def.id}</div>}
              <Markdown text={f.md} />
            </div>))}
        </div>)}

      <div className="card runcard">
        <div className="runtop">
          <h2 style={{ margin: 0 }}>Steps</h2>
          <div className="runfacts">
            {/* Money only when some step HAS a known price — never a label
                for a number nobody recorded. */}
            {totals.priced > 0 && <span><Cost usage={{ costUsd: totals.usd }} />
              {totals.unpriced > 0 && <span className="muted"> + {totals.unpriced} unpriced</span>}</span>}
            {totals.tok > 0 && <span>{tokens(totals.tok)} tokens</span>}
            <span>{stepDefs.length} step{stepDefs.length === 1 ? '' : 's'}</span>
          </div>
        </div>
        <RunTimeline steps={stepDefs} byId={byId} events={events} now={now} selected={current} onPick={setSel}
          skipReason={head.verdict === 'ok' ? 'nothing actionable found' : 'their conditions did not match'} />
      </div>

      <div className="card">
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
          <h2 style={{ margin: 0 }}>{curDef?.name || current}</h2>
          <span className={`badge ${curRun?.status || 'pending'}`}>{curRun?.status || 'pending'}</span>
          {curRun && curRun.status !== 'running' && <button className="ghost" style={{ marginLeft: 'auto' }} onClick={() => act(api.retry(run.id, current))}>Re-run from here</button>}
        </div>
        {curRun?.resolvedBy && <div className="banner ok" style={{ marginTop: 10 }}><Resolved s={curRun} /></div>}
        {curRun?.error && <div className="banner err" style={{ marginTop: 10 }}>{curRun.error}</div>}
        {curSummary
          ? <div style={{ marginTop: 10 }}><Markdown text={curSummary} /></div>
          : <p className="muted" style={{ marginTop: 10 }}>No summary from this step{script ? <> — a script writes one by appending markdown to <span className="mono">$WFX_STEP_SUMMARY</span></> : ''}.</p>}
        {!script && (
          <div className="kv" style={{ marginTop: 10 }}>
            <b>skills</b><span className="mono">{curDef?.skills?.join(', ') || '—'}</span>
            <b>tools</b><span className="mono">{curDef?.tools?.join(', ') || '—'}</span>
            <b>model</b><span className="mono">{curDef?.model || curDef?.provider || 'default'}</span>
            {curUsage?.totalTokens ? <><b>tokens</b><span className="mono">
              {curUsage.promptTokens !== undefined
                ? `${tokens(curUsage.promptTokens)} in · ${tokens(curUsage.completionTokens)} out`
                : tokens(curUsage.totalTokens)}
            </span></> : null}
            {(curUsage?.costUsd !== undefined || curUsage?.costUnknown) && <><b>cost</b><span><Cost usage={curUsage} /></span></>}
          </div>)}
        <details style={{ marginTop: 12 }}>
          <summary><b>Log</b></summary>
          <div style={{ marginTop: 8 }}><EventLog events={events} filter={current || undefined} /></div>
        </details>
        {curRun?.output && (
          <details style={{ marginTop: 8 }}>
            <summary><b>Output</b></summary>
            <pre style={{ marginTop: 8 }}>{JSON.stringify(curRun.output, null, 2)}</pre>
            {!script && <><h3 style={{ marginTop: 10 }}>Output contract</h3><OutputContract schema={curDef?.outputSchema} /></>}
          </details>)}
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
      <div className="muted" style={{ fontSize: 11.5, marginTop: 10 }}>
        {live ? 'live · refreshes every second' : `finished ${new Date(run.updatedAt).toLocaleString()}`}
      </div>
    </>
  )
}
