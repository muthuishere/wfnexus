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
  const [editing, setEditing] = useState<string>()
  const [draft, setDraft] = useState('')
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)

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
  const startEdit = (m: MemoryEntry) => { setEditing(m.id); setDraft(m.body) }
  const save = (m: MemoryEntry) => {
    if (!draft.trim()) return
    setBusy(true)
    api.editMemory(m.id, draft)
      .then(() => { setEditing(undefined); load() })
      .catch(e => setErr(String(e.message || e)))
      .finally(() => setBusy(false))
  }
  const addNote = () => {
    if (!note.trim()) return
    setBusy(true)
    api.addMemoryNote(workflow, note, project || '')
      .then(() => { setNote(''); load() })
      .catch(e => setErr(String(e.message || e)))
      .finally(() => setBusy(false))
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
                  {m.status && <span className="muted" style={{ fontSize: 11 }}>{m.status}</span>}
                </td>
                <td style={{ verticalAlign: 'top' }}>
                  {m.stepId && <span className="muted mono" style={{ fontSize: 11 }}>[{m.stepId}] </span>}
                  {editing === m.id
                    ? <>
                      <textarea className="mono" style={{ width: '100%', minHeight: 80, fontSize: 12 }} value={draft}
                        autoFocus onChange={e => setDraft(e.target.value)} />
                      <div style={{ display: 'flex', gap: 8, marginTop: 6 }}>
                        <button className="small" disabled={busy || !draft.trim()} onClick={() => save(m)}>Save</button>
                        <button className="ghost small" disabled={busy} onClick={() => setEditing(undefined)}>Cancel</button>
                      </div>
                    </>
                    : <pre className="mono" style={{ whiteSpace: 'pre-wrap', margin: 0, fontSize: 12 }}>{m.body}</pre>}
                </td>
                <td className="muted" style={{ width: 150, verticalAlign: 'top', fontSize: 12 }}>
                  {new Date(m.createdAt).toLocaleString()}
                </td>
                <td style={{ width: 130, verticalAlign: 'top', whiteSpace: 'nowrap' }}>
                  {editing !== m.id &&
                    <button className="ghost small" title="Edit" aria-label="Edit entry" onClick={() => startEdit(m)}>✎</button>}{' '}
                  <button className="ghost small danger-text" onClick={() => forget(m)}>Forget</button>
                </td>
              </tr>))}
          </tbody></table>}

      <div style={{ marginTop: 12 }}>
        <textarea style={{ width: '100%', minHeight: 46 }} value={note}
          placeholder="Add a note the next run must read — e.g. “staging is down until Friday; check prod logs instead”"
          onChange={e => setNote(e.target.value)} />
        <button className="small" style={{ marginTop: 6 }} disabled={busy || !note.trim()} onClick={addNote}>Add note</button>
      </div>

      {mem?.preview &&
        <div style={{ marginTop: 12 }}>
          <button className="ghost small" onClick={() => setPreview(p => !p)}>
            {preview ? 'Hide' : 'Show'} what the next run reads
          </button>
          {preview && <pre className="mono" style={{ whiteSpace: 'pre-wrap', fontSize: 12, marginTop: 8 }}>{mem.preview}</pre>}
        </div>}
    </div>
  )
}
