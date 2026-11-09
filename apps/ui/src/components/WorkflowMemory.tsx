import { useCallback, useEffect, useState } from 'react'
import { api, type MemoryView } from '../api'
import Markdown from './Markdown'

/** A workflow's memory — toolnexus's memory hierarchy, one home per workflow:
 *  MEMORY.md (a short index every agent step loads, capped), notes linked
 *  from it as [[slug]] and read on demand, and the dream log (memory/log.md,
 *  append-only: what each nightly dream changed and why). Agents write it
 *  with the `memory` tool; a person edits it here. */
export default function WorkflowMemory({ workflow, project }: { workflow: string; project?: string }) {
  const p = project || ''
  const [mem, setMem] = useState<MemoryView>()
  const [err, setErr] = useState('')
  const [open, setOpen] = useState<string>()       // target being viewed
  const [body, setBody] = useState('')
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState('')
  const [busy, setBusy] = useState('')
  const fail = (e: unknown) => setErr(e instanceof Error ? e.message : String(e))

  const load = useCallback(() => {
    api.workflowMemory(workflow, p).then(m => { setMem(m); setErr('') }).catch(fail)
  }, [workflow, p])
  useEffect(load, [load])

  const view = (target: string) => {
    setOpen(target); setEditing(false)
    if (target === 'index') { setBody(mem?.index || ''); return }
    api.readMemory(workflow, target, p).then(r => setBody(r.body)).catch(e => { setBody(''); fail(e) })
  }
  const edit = (target: string, text: string) => { setOpen(target); setDraft(text); setEditing(true) }
  const save = () => {
    if (!open) return
    setBusy('save')
    api.writeMemory(workflow, open, draft, p)
      .then(() => { setEditing(false); setBody(draft); load() }).catch(fail).finally(() => setBusy(''))
  }
  const remove = (target: string) => {
    if (!confirm(`Delete ${target}?`)) return
    api.deleteMemory(workflow, target, p).then(() => { setOpen(undefined); load() }).catch(fail)
  }
  const dream = () => {
    setBusy('dream')
    api.dreamMemory(workflow, p)
      .then(r => { alert(r.changed ? `Dream changed memory (see the log). ${r.runs} runs checked.` : `Nothing to change. ${r.runs} runs checked.`); load() })
      .catch(fail).finally(() => setBusy(''))
  }

  // [[slug]] → a code span, so the index reads as links to the notes below.
  const linked = (s: string) => s.replace(/\[\[([a-z0-9._-]+)\]\]/g, '`[[$1]]`')

  if (!mem) return <div className="card">{err ? <div className="banner err">{err}</div> : <span className="muted">loading…</span>}</div>
  const notes = mem.notes.filter(n => n.slug !== 'log')
  return (
    <div className="card" style={{ marginBottom: 16 }}>
      <div className="subhead">
        <h2>Memory</h2>
        <span style={{ display: 'flex', gap: 8 }}>
          <button className="ghost small" onClick={() => edit('index', mem.index)}>Edit index</button>
          <button className="small" disabled={!!busy} onClick={dream}>{busy === 'dream' ? 'Dreaming…' : 'Run dream now'}</button>
        </span>
      </div>
      {err && <div className="banner err">{err}</div>}
      {!mem.enabled && <div className="banner">Memory is off for this workflow (<code>memory: false</code>).</div>}

      <h3>MEMORY.md <span className="muted" style={{ fontWeight: 400 }}>· loaded into every agent step</span></h3>
      {mem.index.trim()
        ? <Markdown text={linked(mem.index)} />
        : <p className="muted">Empty. Agents add lines with the <code>memory</code> tool; detail goes in notes.</p>}

      <h3 style={{ marginTop: 16 }}>Notes <span className="muted" style={{ fontWeight: 400 }}>· read on demand</span></h3>
      {notes.length === 0 && <p className="muted">No notes yet.</p>}
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
        {notes.map(n => (
          <button key={n.slug} className={`ghost small ${open === 'note:' + n.slug ? 'active' : ''}`}
            onClick={() => view('note:' + n.slug)} title={`${n.bytes} bytes`}>[[{n.slug}]]</button>))}
        <button className="ghost small" onClick={() => {
          const slug = prompt('New note slug (lowercase, digits, . _ -)')
          if (slug) edit('note:' + slug.trim(), '')
        }}>+ note</button>
      </div>

      {open && (
        <div className="card" style={{ marginTop: 12 }}>
          <div className="subhead">
            <span className="mono">{open === 'index' ? 'MEMORY.md' : open === 'dream' ? 'DREAM.md' : `memory/${open.slice(5)}.md`}</span>
            <span style={{ display: 'flex', gap: 8 }}>
              {!editing && open !== 'note:log' && <button className="ghost small" onClick={() => edit(open, body)}>Edit</button>}
              {!editing && open.startsWith('note:') && open !== 'note:log' &&
                <button className="ghost small danger-text" onClick={() => remove(open)}>Delete</button>}
              <button className="ghost small" onClick={() => setOpen(undefined)}>Close</button>
            </span>
          </div>
          {editing
            ? <>
              <textarea rows={10} style={{ width: '100%' }} value={draft} onChange={e => setDraft(e.target.value)} />
              <div style={{ display: 'flex', gap: 8, marginTop: 8 }}>
                <button className="small" disabled={!!busy} onClick={save}>Save</button>
                <button className="ghost small" onClick={() => setEditing(false)}>Cancel</button>
              </div>
            </>
            : <Markdown text={linked(body)} />}
        </div>)}

      <h3 style={{ marginTop: 16 }}>Dream log <span className="muted" style={{ fontWeight: 400 }}>· nightly check against recent runs</span></h3>
      {mem.log.trim() ? <Markdown text={mem.log} /> : <p className="muted">No dreams have changed anything yet.</p>}
      <p className="muted" style={{ marginTop: 8 }}>
        Dream focus (DREAM.md): {mem.dream.trim() ? <i>{mem.dream.trim().slice(0, 120)}</i> : 'default'} ·{' '}
        <a href="#" onClick={e => { e.preventDefault(); edit('dream', mem.dream) }}>edit</a>
      </p>
    </div>
  )
}
