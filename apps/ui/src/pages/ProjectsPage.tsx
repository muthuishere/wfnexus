import { useCallback, useEffect, useState } from 'react'
import { api, type Project } from '../api'
import DataTable, { type Column } from '../components/DataTable'
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

  const columns: Column<Project>[] = [
    {
      key: 'name', header: 'Project', width: 200,
      value: p => p.name,
      cell: p => (
        <>
          <a href={`#/projects/${encodeURIComponent(p.name)}`} className="mono" style={{ fontWeight: 550 }}>{p.name}</a>
          {p.local && <span className="pill" style={{ marginLeft: 6 }}>this platform</span>}
        </>),
    },
    {
      key: 'workflows', header: 'Workflows', width: 100, align: 'right',
      value: p => p.workflows?.length ?? 0,
      cell: p => p.workflows?.length || <span className="muted">—</span>,
    },
    {
      key: 'runs', header: 'Runs', width: 90, align: 'right',
      value: p => p.runs,
      cell: p => p.runs || <span className="muted">—</span>,
    },
    {
      key: 'last', header: 'Last run', width: 170,
      value: p => p.lastRunAt || '',
      cell: p => p.lastRun
        ? <><span className={`badge ${p.lastRun}`}>{p.lastRun.replace(/_/g, ' ')}</span>{' '}
          <span className="muted" style={{ fontSize: 12 }}>{ago(p.lastRunAt)}</span></>
        : <span className="muted">never</span>,
    },
    {
      key: 'where', header: 'Where it lives',
      value: p => p.url || p.repo || p.dir,
      cell: p => <span className="mono muted" title={p.dir}>{p.url || p.repo || p.dir}</span>,
    },
    {
      key: 'actions', header: '', width: 100, align: 'right', sortable: false,
      cell: p => p.local ? null : (
        <button className="ghost small danger-text" onClick={e => {
          e.stopPropagation()
          if (!confirm(`Forget ${p.name}? Its runs stay in the history and the clone is left on disk.`)) return
          api.removeProject(p.name).then(() => { setNote(`Forgot ${p.name}.`); load() })
            .catch(er => setErr(String(er.message || er)))
        }}>Forget</button>),
    },
  ]

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
        <button onClick={() => setAdding(a => !a)}>{adding ? 'Cancel' : 'Add project'}</button>
      </div>

      {err && <div className="banner err">{err}</div>}
      {note && <div className="banner ok">{note}</div>}
      {adding && <AddProject onDone={m => { setAdding(false); setNote(m); load() }} onError={setErr} />}

      <div className="card">
        {!projects ? <div className="muted">loading…</div> : (
          <DataTable
            rows={projects} columns={columns} getKey={p => p.name}
            onRowClick={p => { location.hash = `#/projects/${encodeURIComponent(p.name)}` }}
            initialSort={{ key: 'last', dir: 'desc' }}
            searchPlaceholder="Search projects…"
            empty="No projects yet. Add a repository that has a .wfx/workflows/ directory." />)}
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
