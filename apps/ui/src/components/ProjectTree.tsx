import { useCallback, useEffect, useMemo, useState } from 'react'
import { api, type Project, type Run } from '../api'
import { ago } from '../lib/time'
import { sameWorkflow, summariseRun } from '../lib/workflow'

/** The hierarchy, always in view: project → workflow → runs.
 *
 *  This replaced the flat "All runs" and "Workflows" lists. A run means nothing
 *  without the workflow it belongs to, and a workflow nothing without the
 *  repository it runs against — so the navigation IS that tree, and where you
 *  are is highlighted in it rather than implied by a breadcrumb alone.
 *
 *  Each workflow shows its latest few runs; the rest are one click away on the
 *  workflow's own page, where they can be searched and filtered. */

const RUNS_SHOWN = 5
const OPEN_KEY = 'wfx.tree.open'

export default function ProjectTree({ route }: { route: string[] }) {
  const [projects, setProjects] = useState<Project[]>()
  const [runs, setRuns] = useState<Record<string, Run[]>>({})
  const [err, setErr] = useState('')
  const [q, setQ] = useState('')
  const [open, setOpen] = useState<Set<string>>(() => readOpen())
  // Where a run lives, when it is older than the recent runs the tree holds.
  const [runHome, setRunHome] = useState<{ id: string; project: string; workflow: string }>()

  const load = useCallback(() => {
    api.projects()
      .then(ps => Promise.all(ps.map(p => api.runs({ project: p.name }).catch(() => [] as Run[])))
        .then(rs => {
          setProjects(ps)
          setRuns(Object.fromEntries(ps.map((p, i) => [p.name, rs[i]])))
          setErr('')
        }))
      .catch(e => setErr(e instanceof Error ? e.message : String(e)))
  }, [])
  useEffect(() => {
    load()
    const t = setInterval(load, 5000)
    return () => clearInterval(t)
  }, [load])

  // What the route points at, in tree terms.
  const runId = route[0] === 'runs' ? route[1] : undefined
  const knownRun = useMemo(() => {
    if (!runId) return undefined
    for (const list of Object.values(runs)) {
      const r = list.find(x => x.id === runId)
      if (r) return { id: r.id, project: r.project || 'local', workflow: r.workflow }
    }
    return undefined
  }, [runs, runId])
  useEffect(() => {
    if (!runId || knownRun || runHome?.id === runId) return
    api.run(runId)
      .then(d => setRunHome({ id: runId, project: d.run.project || 'local', workflow: d.run.workflow }))
      .catch(() => {})
  }, [runId, knownRun, runHome])
  const here = knownRun || (runHome?.id === runId ? runHome : undefined)

  const activeProject = route[0] === 'projects' ? decode(route[1])
    : route[0] === 'runs' ? here?.project
    : undefined
  const activeWorkflow = route[0] === 'projects' ? decode(route[2])
    : route[0] === 'runs' ? here?.workflow
    : route[0] === 'workflows' && route[2] === 'new' ? decode(route[1])
    : undefined

  const toggle = (key: string) => setOpen(prev => {
    const next = new Set(prev)
    if (next.has(key)) next.delete(key); else next.add(key)
    writeOpen(next)
    return next
  })
  // A project is open unless somebody closed it; a workflow is closed unless
  // somebody opened it. The inverse defaults are what keeps a tree with many
  // workflows scannable. Whatever the route points at is always open.
  const projectOpen = (p: string) => !open.has('p-closed:' + p) || p === activeProject || !!q
  const workflowOpen = (p: string, w: string) =>
    open.has(`w:${p}/${w}`) || (p === activeProject && !!activeWorkflow && sameWorkflow(activeWorkflow, w))

  const needle = q.trim().toLowerCase()

  return (
    <aside className="tree">
      <div className="tree-search">
        <input value={q} onChange={e => setQ(e.target.value)} placeholder="Filter workflows…" aria-label="Filter workflows" />
      </div>
      {err && <div className="tree-note err">{err}</div>}
      {!projects && !err && <div className="tree-note">loading…</div>}
      {projects?.length === 0 && <div className="tree-note">No projects yet.</div>}
      <ul role="tree">
        {projects?.map(p => {
          const pRuns = runs[p.name] || []
          const wfs = p.workflows.filter(w => !needle || w.toLowerCase().includes(needle))
          if (needle && !wfs.length) return null
          const pOpen = projectOpen(p.name)
          const pActive = p.name === activeProject && !activeWorkflow
          return (
            <li key={p.name} role="treeitem" aria-expanded={pOpen}>
              <div className={`row project${pActive ? ' active' : ''}`}>
                <button className="chev" aria-label={pOpen ? 'Collapse' : 'Expand'}
                  onClick={() => toggle('p-closed:' + p.name)}>{pOpen ? '▾' : '▸'}</button>
                <a href={`#/projects/${encodeURIComponent(p.name)}`} className="label">
                  <span className="name">{p.name}</span>
                </a>
                <span className="count">{p.workflows.length}</span>
              </div>
              {pOpen && (
                <ul role="group">
                  {wfs.length === 0 && <li className="tree-note indent">no workflows</li>}
                  {wfs.map(w => {
                    const wRuns = pRuns.filter(r => sameWorkflow(r.workflow, w))
                    const last = wRuns[0]
                    const wOpen = workflowOpen(p.name, w)
                    const wActive = p.name === activeProject && !!activeWorkflow && sameWorkflow(activeWorkflow, w) && !runId
                    return (
                      <li key={w} role="treeitem" aria-expanded={wOpen}>
                        <div className={`row workflow${wActive ? ' active' : ''}`}>
                          <button className="chev" aria-label={wOpen ? 'Collapse' : 'Expand'}
                            onClick={() => toggle(`w:${p.name}/${w}`)}>{wOpen ? '▾' : '▸'}</button>
                          <a href={`#/projects/${encodeURIComponent(p.name)}/${encodeURIComponent(w)}`} className="label" title={w}>
                            <span className={`dot ${last?.status || 'never'}`} title={last ? last.status.replace(/_/g, ' ') : 'never run'} />
                            <span className="name mono">{w}</span>
                          </a>
                          {wRuns.length > 0 && <span className="count">{wRuns.length}</span>}
                        </div>
                        {wOpen && (
                          <ul role="group">
                            {wRuns.length === 0 && <li className="tree-note indent2">never run</li>}
                            {wRuns.slice(0, RUNS_SHOWN).map(r => {
                              const about = summariseRun(r)
                              return (
                                <li key={r.id} role="treeitem">
                                  <a href={`#/runs/${r.id}`} className={`row run${r.id === runId ? ' active' : ''}`}
                                    title={`${r.status.replace(/_/g, ' ')} · ${new Date(r.createdAt).toLocaleString()}${about ? ' · ' + about : ''}`}>
                                    <span className={`dot ${r.status}`} />
                                    <span className="name">{about || <span className="mono">{r.id.slice(0, 8)}</span>}</span>
                                    <span className="when">{ago(r.createdAt)}</span>
                                  </a>
                                </li>)
                            })}
                            {wRuns.length > RUNS_SHOWN && (
                              <li>
                                <a className="row more" href={`#/projects/${encodeURIComponent(p.name)}/${encodeURIComponent(w)}`}>
                                  all {wRuns.length} runs →
                                </a>
                              </li>)}
                          </ul>)}
                      </li>)
                  })}
                </ul>)}
            </li>)
        })}
      </ul>
    </aside>
  )
}

function decode(s?: string) { return s ? decodeURIComponent(s) : undefined }

// Which branches a person opened or closed is a per-viewer convenience, so it
// lives in the browser — and every access is guarded, because storage can be
// missing or throw (a private window, blocked site data) and the tree must
// work regardless.
function readOpen(): Set<string> {
  try { return new Set(JSON.parse(localStorage.getItem(OPEN_KEY) || '[]')) } catch { return new Set() }
}
function writeOpen(s: Set<string>) {
  try { localStorage.setItem(OPEN_KEY, JSON.stringify([...s])) } catch { /* not worth failing over */ }
}
