import { useEffect, useState } from 'react'
import { api, type Workflow } from '../api'

export default function WorkflowsPage() {
  const [wfs, setWfs] = useState<Workflow[]>([])
  const [err, setErr] = useState('')
  const load = () => api.workflows().then(setWfs).catch(e => setErr(e.message))
  useEffect(() => { load() }, [])
  return (
    <>
      <div style={{ display: 'flex', alignItems: 'center', marginBottom: 14 }}>
        <h1>Workflows</h1>
        <button className="ghost" style={{ marginLeft: 'auto' }} onClick={() => api.reload().then(setWfs).catch(e => setErr(e.message))}>Reload YAML</button>
      </div>
      {err && <div className="banner err">{err}</div>}
      {wfs.map(w => (
        <div className="card" key={w.name}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
            <div><h2>{w.name}</h2><div className="muted">{w.description}</div><div className="mono muted" style={{ marginTop: 4 }}>{w.path}</div></div>
            <a href={`#/workflows/${w.name}/new`} style={{ marginLeft: 'auto' }}><button>New run</button></a>
          </div>
          <div className="steps" style={{ marginTop: 14 }}>
            {w.steps.map((s, i) => (
              <div className="step" key={s.id} style={{ cursor: 'default' }}>
                <div className="dot" />
                <div style={{ flex: 1 }}>
                  <div><b>{i + 1}. {s.name}</b> {s.requiresApproval && <span className="badge awaiting_approval">approval gate</span>} <span className="muted"> {s.description}</span></div>
                  <div className="chips" style={{ marginTop: 4 }}>
                    {s.skills.map(x => <span key={x}>skill:{x}</span>)}
                    {s.tools.map(x => <span key={x}>tool:{x}</span>)}
                    {(s.mcp || []).map(x => <span key={x}>mcp:{x}</span>)}
                    <span>→ {Object.keys(s.outputSchema?.properties || {}).join(', ')}</span>
                  </div>
                </div>
              </div>
            ))}
          </div>
        </div>
      ))}
    </>
  )
}
