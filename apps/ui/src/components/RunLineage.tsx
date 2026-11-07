import type { Run } from '../api'

// ADR 0026: a `start:` step starts other workflows as runs of their own. The
// run page names the parent that started this run, and lists the runs this
// one started.

export function ParentLine({ run }: { run: Run }) {
  if (!run.parentRunId && !run.conversation) return null
  return (
    <>
      {run.parentRunId && (
        <div className="muted" style={{ fontSize: 'var(--fs-xs)', marginTop: 3 }} data-testid="run-parent">
          started by <a href={`#/runs/${run.parentRunId}`} className="mono">{run.parentRunId.slice(0, 8)}</a>
          {run.parentStepId && <> at step <span className="mono">{run.parentStepId}</span></>}
          {run.parentMode === 'wait' && <> (waits for it)</>}
          {run.parentMode === 'event' && <> (continues on its completion event)</>}
        </div>)}
      {run.conversation && (
        <div className="muted" style={{ fontSize: 'var(--fs-xs)', marginTop: 3 }}>
          conversation <span className="mono">{run.conversation}</span>
        </div>)}
    </>
  )
}

export function ChildRuns({ runs }: { runs?: Run[] }) {
  if (!runs?.length) return null
  return (
    <div className="card" data-testid="run-children">
      <h2>Started runs</h2>
      {runs.map(c => (
        <div key={c.id} style={{ display: 'flex', gap: 8, alignItems: 'center', marginTop: 4 }}>
          <span className={`badge ${c.status}`}>{c.status.replace(/_/g, ' ')}</span>
          <a href={`#/runs/${c.id}`} className="mono">{c.workflow}</a>
          <span className="muted mono" style={{ fontSize: 'var(--fs-2xs)' }}>
            {c.id.slice(0, 8)}{c.parentStepId ? ` · from ${c.parentStepId}` : ''}
          </span>
        </div>))}
    </div>
  )
}
