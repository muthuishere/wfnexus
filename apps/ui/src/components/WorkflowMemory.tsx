import { useCallback, useEffect, useState } from 'react'
import { api, type MemoryEntry, type MemoryList } from '../api'

/** What previous runs of this workflow learned — workflow memory.
 *
 *  On by default: every finished run leaves a record (inputs, outcome, each
 *  step's verdict or error, human decisions) and agents add lessons with the
 *  `remember` tool. Every agent step of the next run reads the section shown
 *  under "What the next run reads". `memory: false` in the YAML turns it off. */
export default function WorkflowMemory({ workflow, project }: { workflow: string; project?: string }) {
  const [mem, setMem] = useState<MemoryList>()
  const [err, setErr] = useState('')
  const [preview, setPreview] = useState(false)

  const load = useCallback(() => {
    api.workflowMemory(workflow, project || '')
      .then(m => { setMem(m); setErr('') })
      .catch(e => setErr(e instanceof Error ? e.message : String(e)))
  }, [workflow, project])
  useEffect(load, [load])

  const forget = (m: MemoryEntry) => {
    if (!confirm('Forget this entry? Later runs will no longer read it.')) return
    api.deleteMemory(m.id).then(load).catch(e => setErr(String(e.message || e)))
  }
  const forgetAll = () => {
    if (!confirm(`Forget everything ${workflow} remembers? The next run starts blind.`)) return
    api.forgetWorkflowMemory(workflow, project || '').then(load).catch(e => setErr(String(e.message || e)))
  }

  return (
    <div className="card" style={{ marginBottom: 16 }}>
      <div className="subhead">
        <h2>Memory</h2>
        {mem && mem.entries.length > 0 &&
          <button className="ghost small danger-text" onClick={forgetAll}>Forget all</button>}
      </div>
      <p className="muted" style={{ marginBottom: 12 }}>
        What previous runs of this workflow learned. Every finished run leaves a record, agents add
        lessons with the <span className="mono">remember</span> tool, and every agent step of the next
        run reads it (also as <span className="mono">{'{{ .Memory }}'}</span>). Secrets are scrubbed before
        anything is stored. Turn it off with <span className="mono">memory: false</span>.
      </p>

      {err && <div className="banner err">{err}</div>}
      {mem?.enabled === false && <div className="banner">Memory is off for this workflow (memory: false).</div>}

      {!mem ? <div className="muted">loading…</div>
        : mem.entries.length === 0
          ? <div className="muted">Nothing remembered yet — the first finished run will leave a record.</div>
          : <table className="rows"><tbody>
            {mem.entries.map(m => (
              <tr key={m.id}>
                <td style={{ width: 120, verticalAlign: 'top' }}>
                  <span className="pill">{m.kind === 'note' ? 'lesson' : m.kind}</span>{' '}
                  {m.status && <span className="muted" style={{ fontSize: 'var(--fs-2xs)' }}>{m.status}</span>}
                </td>
                <td style={{ verticalAlign: 'top' }}>
                  {m.stepId && <span className="muted mono" style={{ fontSize: 'var(--fs-2xs)' }}>[{m.stepId}] </span>}
                  <pre className="mono" style={{ whiteSpace: 'pre-wrap', margin: 0, fontSize: 'var(--fs-2xs)' }}>{m.body}</pre>
                </td>
                <td className="muted" style={{ width: 150, verticalAlign: 'top', fontSize: 'var(--fs-2xs)' }}>
                  {new Date(m.createdAt).toLocaleString()}
                </td>
                <td style={{ width: 80, verticalAlign: 'top' }}>
                  <button className="ghost small danger-text" onClick={() => forget(m)}>Forget</button>
                </td>
              </tr>))}
          </tbody></table>}

      {mem?.preview &&
        <div style={{ marginTop: 12 }}>
          <button className="ghost small" onClick={() => setPreview(p => !p)}>
            {preview ? 'Hide' : 'Show'} what the next run reads
          </button>
          {preview && <pre className="mono" style={{ whiteSpace: 'pre-wrap', fontSize: 'var(--fs-2xs)', marginTop: 8 }}>{mem.preview}</pre>}
        </div>}
    </div>
  )
}
