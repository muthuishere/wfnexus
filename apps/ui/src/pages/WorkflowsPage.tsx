import { useEffect, useState } from 'react'
import { api, type Workflow } from '../api'
import StepHarness from '../components/StepHarness'

export default function WorkflowsPage() {
  const [wfs, setWfs] = useState<Workflow[]>()
  const [err, setErr] = useState('')
  const [open, setOpen] = useState<Record<string, boolean>>({})
  useEffect(() => { api.workflows().then(setWfs).catch(e => setErr(e.message)) }, [])
  const reload = () => api.reload().then(w => { setWfs(w); setErr('') }).catch(e => setErr(e.message))

  return (
    <>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 14 }}>
        <div>
          <h1>Workflows</h1>
          <div className="muted">Each step is a whole agent — its own soul, skills, tools, team, budget and output contract.</div>
        </div>
        <button className="ghost" style={{ marginLeft: 'auto' }} onClick={reload}>Reload YAML</button>
      </div>
      {err && <div className="banner err">{err}</div>}
      {!wfs && !err && <div className="muted">loading…</div>}
      {wfs?.length === 0 && <div className="card muted">No workflows loaded. Drop a YAML file in <span className="mono">workflows/</span> and hit Reload.</div>}
      {wfs?.map(w => {
        const shown = open[w.name] !== false
        return (
          <div className="card" key={w.name}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}>
              <div style={{ minWidth: 0 }}>
                <h2>{w.name} <span className="muted" style={{ fontWeight: 400, fontSize: 13 }}>{w.steps.length} steps</span></h2>
                <div className="muted">{w.description}</div>
                <div className="mono muted" style={{ marginTop: 4 }}>{w.path}</div>
              </div>
              <div className="actions" style={{ marginLeft: 'auto', marginTop: 0 }}>
                <button className="ghost" onClick={() => setOpen({ ...open, [w.name]: !shown })}>{shown ? 'Collapse' : 'Expand'} steps</button>
                <a href={`#/workflows/${w.name}/new`}><button>New run</button></a>
              </div>
            </div>
            {shown && (
              <div className="agents">
                {w.steps.map((s, i) => <StepHarness key={s.id} step={s} index={i} />)}
              </div>)}
          </div>)
      })}
    </>
  )
}
