import { useEffect, useState } from 'react'
import { api, type Run } from '../api'

export default function RunsPage() {
  const [runs, setRuns] = useState<Run[]>([])
  useEffect(() => { api.runs().then(setRuns); const t = setInterval(() => api.runs().then(setRuns), 4000); return () => clearInterval(t) }, [])
  return (
    <>
      <div style={{ display: 'flex', alignItems: 'center', marginBottom: 14 }}><h1>Runs</h1><a href="#/workflows" style={{ marginLeft: 'auto' }}><button>New run</button></a></div>
      <div className="card">
        {runs.length === 0 ? <div className="muted">No runs yet. Start one from a workflow.</div> : (
          <table>
            <thead><tr><th>Workflow</th><th>Title</th><th>Status</th><th>Step</th><th>Started</th></tr></thead>
            <tbody>{runs.map(r => (
              <tr className="row" key={r.id} onClick={() => location.hash = `#/runs/${r.id}`}>
                <td className="mono">{r.workflow}</td><td>{r.input?.title || <span className="muted mono">{r.id.slice(0, 8)}</span>}</td>
                <td><span className={`badge ${r.status}`}>{r.status.replace('_', ' ')}</span></td><td className="mono">{r.currentStep}</td>
                <td className="muted">{new Date(r.createdAt).toLocaleString()}</td>
              </tr>))}</tbody>
          </table>)}
      </div>
    </>
  )
}
