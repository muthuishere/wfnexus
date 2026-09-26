import { useCallback, useEffect, useState } from 'react'
import { api, type Project } from '../api'
import { href } from '../lib/routes'
import { ago } from '../lib/time'

/** The dashboard, and the top of the hierarchy: project → workflow → runs.
 *
 *  A project is a repository whose `.wfx/workflows/` this platform runs — the
 *  same arrangement as `.github/workflows/`, and the same hierarchy GitHub
 *  Actions has. A flat global run list stops meaning anything the moment there
 *  is a second repository, and "which repo was that against" is the first
 *  question anyone asks about a run. */
export default function ProjectsPage() {
  const [projects, setProjects] = useState<Project[]>()
  const [err, setErr] = useState('')
  const [note, setNote] = useState('')
  const [adding, setAdding] = useState(false)

  const load = useCallback(() => {
    api.projects().then(p => { setProjects(p); setErr('') })
      .catch(e => setErr(e instanceof Error ? e.message : String(e)))
  }, [])
  useEffect(load, [load])

  return (
    <>
      <div className="head">
        <div>
          <h1>Projects</h1>
          <p>
            A project is a repository whose <span className="mono">.wfx/workflows/</span> this platform
            runs — the same arrangement as <span className="mono">.github/workflows/</span>. Its workflows
            and every run of them belong to it.
          </p>
        </div>
        <div style={{ display: 'flex', gap: 8 }}>
          <button className="ghost" onClick={() => api.reload()
            .then(w => { setNote(`Reloaded — ${w.length} workflow(s).`); load() })
            .catch(e => setErr(e instanceof Error ? e.message : String(e)))}>Reload from disk</button>
          <button onClick={() => setAdding(a => !a)}>{adding ? 'Cancel' : 'Add project'}</button>
        </div>
      </div>

      {err && <div className="banner err">{err}</div>}
      {note && <div className="banner ok">{note}</div>}
      {adding && <AddProject onDone={m => { setAdding(false); setNote(m); load() }} onError={setErr} />}

      {/* A list, not a table: a person comes here to pick one and open it.
          Everything else about a project is on its own page. */}
      <div className="card flush">
        {!projects && <div className="empty">loading…</div>}
        {projects?.length === 0 && <div className="empty">No projects yet. Add a repository that has a <span className="mono">.wfx/workflows/</span> directory.</div>}
        <ul className="wflist projects">
          {projects?.map(p => (
            <li key={p.name} onClick={() => { location.hash = href.project(p.name) }}>
              <span className={`dot ${p.lastRun || 'never'}`} />
              <div className="wfmain">
                <a href={href.project(p.name)} className="wfname" onClick={e => e.stopPropagation()}>{p.name}</a>
                {p.local && <span className="pill" style={{ marginLeft: 8 }}>this platform</span>}
                {!!p.problems?.length && <span className="badge failed" style={{ marginLeft: 8 }}>{p.problems.length} did not load</span>}
                <div className="wfdesc mono" title={p.dir}>{p.url || p.repo || p.dir}</div>
              </div>
              <div className="wfmeta">
                <span>{p.workflows?.length || 0} workflow{p.workflows?.length === 1 ? '' : 's'}</span>
                <span>·</span>
                <span>{p.runs || 0} run{p.runs === 1 ? '' : 's'}</span>
              </div>
              <div className="wflast">
                {p.lastRun
                  ? <><span className={`badge ${p.lastRun}`}>{p.lastRun.replace(/_/g, ' ')}</span><span className="muted">{ago(p.lastRunAt)}</span></>
                  : <span className="muted">never run</span>}
              </div>
              <span onClick={e => e.stopPropagation()}>
                {!p.local && (
                  <button className="ghost small danger-text" onClick={() => {
                    if (!confirm(`Forget ${p.name}? Its runs stay in the history and the clone is left on disk.`)) return
                    api.removeProject(p.name).then(() => { setNote(`Forgot ${p.name}.`); load() })
                      .catch(er => setErr(String(er.message || er)))
                  }}>Forget</button>)}
              </span>
            </li>))}
        </ul>
      </div>

      {projects?.some(p => p.problems?.length) && (
        <div className="card">
          <div className="subhead"><h2>Workflows that did not load</h2></div>
          <p className="muted" style={{ marginBottom: 10 }}>
            One broken file does not stop the platform booting, so it is recorded here instead.
          </p>
          <table className="rows"><tbody>
            {projects.flatMap(p => (p.problems || []).map((pr, i) => (
              <tr key={p.name + i}>
                <td className="mono">{p.name}</td>
                <td className="mono muted">{pr.location}</td>
                <td>{pr.reason}</td>
              </tr>)))}
          </tbody></table>
        </div>)}
    </>
  )
}

function AddProject({ onDone, onError }: { onDone: (msg: string) => void; onError: (e: string) => void }) {
  const [repo, setRepo] = useState('')
  const [name, setName] = useState('')
  const [branch, setBranch] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = () => {
    setBusy(true)
    api.addProject({ repo: repo.trim(), name: name.trim() || undefined, branch: branch.trim() || undefined })
      .then(p => onDone(`Added ${p.name} — ${p.workflows?.length ?? 0} workflow(s).`))
      .catch(e => onError(e instanceof Error ? e.message : String(e)))
      .finally(() => setBusy(false))
  }

  return (
    <div className="card">
      <div className="subhead"><h2>Add a project</h2></div>
      <div className="grid2">
        <div className="bfield">
          <label>Repository</label>
          <input className="mono" value={repo} onChange={e => setRepo(e.target.value)}
            placeholder="https://github.com/you/repo  ·  or  /path/to/checkout" />
          <div className="hint">
            A URL is cloned once. A local path is used <b>in place</b>, so you can edit a workflow
            and run it again without pushing.
          </div>
        </div>
        <div className="bfield">
          <label>Name</label>
          <input className="mono" value={name} onChange={e => setName(e.target.value)} placeholder="taken from the repository" />
          <div className="hint">Half of a qualified workflow name, as in <span className="mono">name/workflow</span>.</div>
        </div>
        <div className="bfield">
          <label>Branch <span className="muted">(cloned repositories only)</span></label>
          <input className="mono" value={branch} onChange={e => setBranch(e.target.value)} placeholder="default branch" />
        </div>
      </div>
      <button disabled={!repo.trim() || busy} onClick={submit}>{busy ? 'Adding…' : 'Add project'}</button>
      <div className="hint">
        The repository needs a <span className="mono">.wfx/workflows/</span> directory; one without is
        refused rather than added as something that does nothing.
      </div>
    </div>
  )
}
