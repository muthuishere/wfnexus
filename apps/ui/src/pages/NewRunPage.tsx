import { useEffect, useState } from 'react'
import { api, type Workflow } from '../api'

// Renders a form from the workflow's input_schema (flat object of strings/booleans/numbers).
export default function NewRunPage({ name }: { name: string }) {
  const [wf, setWf] = useState<Workflow>()
  const [vals, setVals] = useState<Record<string, any>>({})
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    api.workflow(name).then(w => {
      setWf(w)
      const d: Record<string, any> = {}
      for (const [k, p] of Object.entries<any>(w.inputSchema?.properties || {})) if (p.default !== undefined) d[k] = p.default
      setVals(d)
    }).catch(e => setErr(e.message))
  }, [name])
  if (!wf) return <div className="muted">{err || 'loading…'}</div>
  const props = Object.entries<any>(wf.inputSchema?.properties || {})
  const required: string[] = wf.inputSchema?.required || []
  const submit = async () => {
    setBusy(true); setErr('')
    try {
      const body: Record<string, any> = {}
      for (const [k, v] of Object.entries(vals)) if (v !== '' && v !== undefined) body[k] = v
      const run = await api.createRun(wf.name, body)
      location.hash = `#/runs/${run.id}`
    } catch (e: any) { setErr(e.message) } finally { setBusy(false) }
  }
  return (
    <div className="card" style={{ maxWidth: 760 }}>
      <h1>New run · {wf.name}</h1>
      <div className="muted">{wf.description}</div>
      {err && <div className="banner err" style={{ marginTop: 12 }}>{err}</div>}
      {props.map(([k, p]) => (
        <div key={k}>
          <label>{p.title || k}{required.includes(k) && ' *'}</label>
          {p.type === 'boolean' ? <input type="checkbox" style={{ width: 'auto' }} checked={!!vals[k]} onChange={e => setVals({ ...vals, [k]: e.target.checked })} />
            : p.format === 'textarea' ? <textarea value={vals[k] ?? ''} onChange={e => setVals({ ...vals, [k]: e.target.value })} />
            : p.enum ? <select value={vals[k] ?? ''} onChange={e => setVals({ ...vals, [k]: e.target.value })}><option value="">—</option>{p.enum.map((o: string) => <option key={o}>{o}</option>)}</select>
            : <input type={p.type === 'number' || p.type === 'integer' ? 'number' : 'text'} value={vals[k] ?? ''} onChange={e => setVals({ ...vals, [k]: p.type === 'number' || p.type === 'integer' ? Number(e.target.value) : e.target.value })} />}
          {p.description && <div className="muted" style={{ fontSize: 12 }}>{p.description}</div>}
        </div>
      ))}
      <div className="actions"><button disabled={busy || required.some(k => !vals[k])} onClick={submit}>{busy ? 'starting…' : 'Start run'}</button><a href="#/workflows"><button className="ghost">Cancel</button></a></div>
    </div>
  )
}
