import { useCallback, useEffect, useState } from 'react'
import { api, type RemoteRefs, type SkillFile, type SkillSourceInfo, type SkillSourceReport } from '../api'
import { ago } from '../lib/time'

const msg = (e: unknown) => (e instanceof Error ? e.message : String(e))
const short = (sha: string) => sha.slice(0, 12)

/** The skill registry's git side: repositories of skills imported at a branch,
 *  tag or commit. A branch follows its remote on Sync; a tag or a commit is a
 *  pin, and Sync says so instead of pretending to fetch. Every action reports
 *  what it added, removed or changed — a registry that changes silently is one
 *  nobody can trust a step's `skills:` against. */
export default function SkillRegistryPage({ source }: { source?: string }) {
  const [list, setList] = useState<SkillSourceInfo[]>()
  const [err, setErr] = useState('')
  const [report, setReport] = useState<SkillSourceReport>()
  const [importing, setImporting] = useState(false)
  const [busy, setBusy] = useState('')

  const load = useCallback(() => {
    api.skillSources().then(l => { setList(l); setErr('') }).catch(e => setErr(msg(e)))
  }, [])
  useEffect(load, [load])

  const act = (name: string, p: Promise<SkillSourceReport>) => {
    setBusy(name); setErr(''); setReport(undefined)
    p.then(r => { setReport(r); load() }).catch(e => setErr(msg(e))).finally(() => setBusy(''))
  }

  const open = list?.find(s => s.name === source)

  return (
    <>
      <div className="head">
        <div>
          <h1>Skill registry</h1>
          <p>
            Git repositories of skills. Every directory under a source's path that holds
            a <span className="mono">SKILL.md</span> becomes a skill a step can name. A source is pinned
            to a branch, a tag or a commit, and the first root to claim a name keeps it.
          </p>
        </div>
        <div style={{ display: 'flex', gap: 8 }}>
          <a href="#/skills" style={{ alignSelf: 'center' }}>All skills &amp; tools</a>
          <button onClick={() => setImporting(i => !i)}>{importing ? 'Cancel' : 'Import from GitHub'}</button>
        </div>
      </div>

      {err && <div className="banner err" role="alert">{err}</div>}
      {report && <Report r={report} />}
      {importing && (
        <ImportForm onDone={r => { setImporting(false); setReport(r); load() }} onError={setErr} />)}

      <div className="card flush">
        {!list && <div className="empty">loading…</div>}
        {list?.length === 0 && <div className="empty">No skill sources yet. Import a repository of skills from GitHub.</div>}
        {!!list?.length && (
          <table className="rows" aria-label="Skill sources">
            <thead><tr>
              <th>Source</th><th>Repository</th><th>Ref</th><th>Commit</th><th>Skills</th><th>Synced</th><th />
            </tr></thead>
            <tbody>
              {list.map(s => (
                <SourceRow key={s.name} s={s} busy={busy === s.name}
                  onSync={() => act(s.name, api.refreshSkillSource(s.name))}
                  onRef={ref => act(s.name, api.setSkillSourceRef(s.name, ref))}
                  onRemove={() => {
                    if (!confirm(`Remove ${s.name}? Its ${s.count} skill(s) are unloaded and the checkout is deleted.`)) return
                    act(s.name, api.removeSkillSource(s.name))
                    if (source === s.name) location.hash = '#/registry'
                  }} />))}
            </tbody>
          </table>)}
      </div>

      {source && open && <SourceSkills key={open.name + open.commit} s={open} />}
      {source && list && !open && <div className="banner warn">No skill source named {source}.</div>}
    </>
  )
}

function Report({ r }: { r: SkillSourceReport }) {
  return (
    <div className={`banner ${r.pinned ? 'warn' : 'ok'}`} aria-label="Result">
      <div>{r.message}</div>
      {!!(r.added.length + r.removed.length + r.updated.length) && (
        <div className="mono" style={{ marginTop: 6, fontSize: 12.5 }}>
          {r.added.map(n => <div key={'a' + n}>+ {n}</div>)}
          {r.removed.map(n => <div key={'r' + n}>− {n}</div>)}
          {r.updated.map(n => <div key={'u' + n}>~ {n}</div>)}
        </div>)}
      {!!r.source.clashes?.length && (
        <div style={{ marginTop: 6 }}>
          {r.source.clashes.map(c => <div key={c.skill}>✗ {c.skill}: already provided by {c.ownedBy} (theirs is kept)</div>)}
        </div>)}
    </div>
  )
}

function ImportForm({ onDone, onError }: { onDone: (r: SkillSourceReport) => void; onError: (e: string) => void }) {
  const [url, setUrl] = useState('')
  const [ref, setRef] = useState('')
  const [path, setPath] = useState('skills')
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const submit = () => {
    setBusy(true); onError('')
    api.importSkillSource({ url: url.trim(), ref: ref.trim() || undefined, path: path.trim() || 'skills', name: name.trim() || undefined })
      .then(onDone).catch(e => onError(msg(e))).finally(() => setBusy(false))
  }
  return (
    <div className="card connect" aria-label="Import skills">
      <div className="subhead"><h2>Import from GitHub</h2></div>
      <div className="grid2">
        <div className="bfield">
          <label htmlFor="ss-url">Repository URL</label>
          <input id="ss-url" className="mono" value={url} autoFocus onChange={e => setUrl(e.target.value)}
            placeholder="https://github.com/you/skills" />
        </div>
        <div className="bfield">
          <label htmlFor="ss-ref">Branch, tag or commit</label>
          <input id="ss-ref" className="mono" value={ref} onChange={e => setRef(e.target.value)} placeholder="the default branch" />
        </div>
        <div className="bfield">
          <label htmlFor="ss-path">Path</label>
          <input id="ss-path" className="mono" value={path} onChange={e => setPath(e.target.value)} placeholder="skills" />
        </div>
        <div className="bfield">
          <label htmlFor="ss-name">Name (optional)</label>
          <input id="ss-name" className="mono" value={name} onChange={e => setName(e.target.value)} placeholder="the repository's name" />
        </div>
      </div>
      <div className="actions">
        <button disabled={!url.trim() || busy} onClick={submit}>{busy ? 'Importing…' : 'Import'}</button>
      </div>
    </div>
  )
}

function SourceRow({ s, busy, onSync, onRef, onRemove }: {
  s: SkillSourceInfo; busy: boolean; onSync: () => void; onRef: (ref: string) => void; onRemove: () => void
}) {
  const [choosing, setChoosing] = useState(false)
  const [refs, setRefs] = useState<RemoteRefs>()
  const [refsErr, setRefsErr] = useState('')
  const [pick, setPick] = useState('')
  const [sha, setSha] = useState('')
  useEffect(() => {
    if (!choosing || refs) return
    api.skillSourceRefs(s.name).then(setRefs).catch(e => setRefsErr(msg(e)))
  }, [choosing, refs, s.name])
  const target = sha.trim() || pick
  return (
    <>
      <tr>
        <td><a className="mono" href={`#/registry/${encodeURIComponent(s.name)}`} style={{ fontWeight: 500 }}>{s.name}</a></td>
        <td className="mono muted" title={s.url}>{s.url}{s.path !== '.' ? ` · ${s.path}` : ''}</td>
        <td><span className="pill">{s.refKind}</span> <span className="mono">{s.ref}</span></td>
        <td className="mono">{short(s.commit)}</td>
        <td>
          {s.count}
          {!!s.clashes?.length && <span className="badge awaiting_approval" style={{ marginLeft: 6 }}
            title={s.clashes.map(c => `${c.skill} — kept by ${c.ownedBy}`).join('\n')}>{s.clashes.length} clash</span>}
        </td>
        <td className="muted">{s.syncedAt ? ago(s.syncedAt) : '—'}</td>
        <td style={{ whiteSpace: 'nowrap' }}>
          <button className="ghost small" disabled={busy} onClick={onSync}
            title={s.refKind === 'branch' ? `Pull ${s.ref}` : `Pinned to ${s.refKind} ${s.ref}`}>{busy ? '…' : 'Sync'}</button>
          <button className="ghost small" disabled={busy} onClick={() => setChoosing(c => !c)}>Change ref</button>
          <button className="ghost small danger-text" disabled={busy} onClick={onRemove}>Remove</button>
        </td>
      </tr>
      {choosing && (
        <tr><td colSpan={7}>
          <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }} aria-label={`Change ref of ${s.name}`}>
            <select aria-label="Branch or tag" value={pick} onChange={e => setPick(e.target.value)}>
              <option value="">{refs ? 'pick a branch or tag…' : refsErr ? 'could not list refs' : 'loading refs…'}</option>
              {!!refs?.branches.length && <optgroup label="Branches">
                {refs.branches.map(b => <option key={'b' + b} value={b}>{b}</option>)}
              </optgroup>}
              {!!refs?.tags.length && <optgroup label="Tags">
                {refs.tags.map(t => <option key={'t' + t} value={t}>{t}</option>)}
              </optgroup>}
            </select>
            <span className="muted">or</span>
            <input className="mono" aria-label="Commit" placeholder="commit sha" value={sha} onChange={e => setSha(e.target.value)} />
            <button className="small" disabled={!target || busy} onClick={() => { onRef(target); setChoosing(false) }}>Switch</button>
            {refsErr && <span className="muted">{refsErr}</span>}
          </div>
        </td></tr>)}
    </>
  )
}

function SourceSkills({ s }: { s: SkillSourceInfo }) {
  const [files, setFiles] = useState<SkillFile[]>()
  const [err, setErr] = useState('')
  const [open, setOpen] = useState('')
  useEffect(() => {
    api.skillSourceSkills(s.name).then(setFiles).catch(e => setErr(msg(e)))
  }, [s.name, s.commit])
  return (
    <div className="card" aria-label={`Skills in ${s.name}`}>
      <div className="subhead">
        <h2>{s.name}</h2>
        <span className="muted mono" style={{ marginLeft: 'auto', fontSize: 12 }}>{s.refKind} {s.ref} · {short(s.commit)}</span>
      </div>
      {err && <div className="banner err">{err}</div>}
      {!files && !err && <div className="empty">loading…</div>}
      {files?.length === 0 && <div className="empty">No SKILL.md under {s.path} at this ref.</div>}
      <ul className="wflist">
        {files?.map(f => (
          <li key={f.path} style={{ display: 'block', cursor: 'default' }}>
            <div style={{ display: 'flex', gap: 8, alignItems: 'baseline' }}>
              <button className="ghost small" onClick={() => setOpen(o => o === f.name ? '' : f.name)}>{open === f.name ? 'Hide' : 'SKILL.md'}</button>
              <span className="mono wfname">{f.name}</span>
              {!f.loaded && <span className="badge awaiting_approval" title="An earlier root already provides this name">shadowed</span>}
              <span className="wfdesc">{f.description}</span>
            </div>
            {open === f.name && (
              // Read-only text: a skill's files are data, so it is shown, never interpreted as HTML.
              <pre className="mono" aria-label={`${f.name} SKILL.md`}
                style={{ whiteSpace: 'pre-wrap', maxHeight: 420, overflow: 'auto', marginTop: 8 }}>{f.content}</pre>)}
          </li>))}
      </ul>
    </div>
  )
}
