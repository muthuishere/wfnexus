import { useCallback, useEffect, useMemo, useState } from 'react'
import EnvStore from '../components/EnvStore'
import StateStore from '../components/StateStore'
import Crumbs, { Tabs } from '../components/Crumbs'
import { api, type Project, type Run, type Workflow } from '../api'
import DataTable, { type Column, type Filter } from '../components/DataTable'
import { ago } from '../lib/time'
import { href, shortName } from '../lib/routes'
import { canDispatch, clip, sameWorkflow, summariseRun, triggerNames } from '../lib/workflow'

type Tab = 'workflows' | 'runs' | 'settings'

/** One project, opened. What a person came here for is almost always one of
 *  its workflows, so that is what the page leads with; the project-wide run
 *  history and the project's settings are a tab away rather than stacked
 *  underneath, where they used to push the workflows off the screen. */
export default function ProjectPage({ name }: { name: string }) {
  const [project, setProject] = useState<Project>()
  const [workflows, setWorkflows] = useState<Workflow[]>([])
  const [runs, setRuns] = useState<Run[]>([])
  const [err, setErr] = useState('')
  const [tab, setTab] = useState<Tab>('workflows')
  const [q, setQ] = useState('')

  const load = useCallback(() => {
    Promise.all([api.project(name), api.workflows(), api.runs({ project: name })])
      .then(([p, w, r]) => { setProject(p); setWorkflows(w); setRuns(r); setErr('') })
      .catch(e => setErr(e instanceof Error ? e.message : String(e)))
  }, [name])
  useEffect(() => {
    load()
    const t = setInterval(load, 5000)
    return () => clearInterval(t)
  }, [load])

  // Only this project's workflows: a definition carries the source it came
  // from, so the filter is the same one the engine uses rather than a guess.
  const mine = useMemo(
    () => workflows.filter(w => (w.source || 'local') === name),
    [workflows, name])
  const shown = useMemo(() => {
    const needle = q.trim().toLowerCase()
    const hit = (w: Workflow) => !needle || w.name.includes(needle) || (w.description || '').toLowerCase().includes(needle)
    const last = (w: Workflow) => runs.find(r => sameWorkflow(r.workflow, w.name))?.createdAt || ''
    // Most recently run first, then the never-run ones by name: what happened
    // lately is what a person opening a project wants to see first.
    return mine.filter(hit).sort((a, b) => last(b).localeCompare(last(a)) || a.name.localeCompare(b.name))
  }, [mine, runs, q])

  if (err) return <div className="banner err">{err}</div>
  if (!project) return <div className="muted">loading…</div>

  const runColumns: Column<Run>[] = [
    {
      key: 'status', header: 'Status', width: 130,
      value: r => r.status,
      cell: r => <span className={`badge ${r.status}`}>{r.status.replace(/_/g, ' ')}</span>,
    },
    {
      key: 'workflow', header: 'Workflow', width: 180,
      value: r => r.workflow,
      cell: r => <span className="mono">{shortName(r.workflow)}</span>,
    },
    {
      key: 'what', header: 'What it is about',
      value: r => summariseRun(r),
      cell: r => {
        const s = summariseRun(r)
        return s ? <span title={s}>{clip(s, 80)}</span> : <span className="muted mono">{r.id.slice(0, 8)}</span>
      },
    },
    {
      key: 'step', header: 'Step', width: 150,
      value: r => r.currentStep || '',
      cell: r => r.currentStep ? <span className="mono">{r.currentStep}</span> : <span className="muted">—</span>,
    },
    {
      key: 'created', header: 'Started', width: 110,
      value: r => r.createdAt,
      cell: r => <span className="muted" title={new Date(r.createdAt).toLocaleString()}>{ago(r.createdAt)}</span>,
    },
  ]
  const runFilters: Filter<Run>[] = [
    { key: 'status', label: 'statuses', options: [...new Set(runs.map(r => r.status))].sort(), match: (r, v) => r.status === v },
    { key: 'workflow', label: 'workflows', options: [...new Set(runs.map(r => r.workflow))].sort(), match: (r, v) => r.workflow === v },
  ]

  return (
    <>
      <Crumbs items={[['Projects', href.projects()]]} />
      <div className="head">
        <div style={{ minWidth: 0 }}>
          <h1>{project.name}</h1>
          <p className="mono">{project.url || project.repo || project.dir}</p>
        </div>
        <a href={href.newWorkflow(name)}><button>New workflow</button></a>
      </div>

      {!!project.problems?.length && (
        <div className="banner err">
          <strong>{project.problems.length} workflow(s) in this project did not load</strong>
          <ul style={{ margin: '6px 0 0', paddingLeft: 18 }}>
            {project.problems.map((p, i) => <li key={i}><span className="mono">{p.location}</span> — {p.reason}</li>)}
          </ul>
        </div>)}

      <Tabs<Tab> value={tab} onChange={setTab} tabs={[
        { key: 'workflows', label: 'Workflows', count: mine.length },
        { key: 'runs', label: 'Runs', count: project.runs },
        { key: 'settings', label: 'Settings' },
      ]} />

      {tab === 'workflows' && (
        <div className="card flush">
          {mine.length > 6 && (
            <div className="listsearch">
              <input value={q} onChange={e => setQ(e.target.value)} placeholder="Search workflows…" aria-label="Search workflows" />
            </div>)}
          {mine.length === 0 && (
            <div className="empty">
              No workflows yet. <a href={href.newWorkflow(name)}>Create one</a>, or add YAML files to
              {' '}<span className="mono">.wfx/workflows/</span> in this repository.
            </div>)}
          <ul className="wflist">
            {shown.map(w => {
              const wRuns = runs.filter(r => sameWorkflow(r.workflow, w.name))
              const last = wRuns[0]
              return (
                <li key={w.name} onClick={() => { location.hash = href.workflow(name, w.name) }}>
                  <span className={`dot ${last?.status || 'never'}`} title={last ? last.status.replace(/_/g, ' ') : 'never run'} />
                  <div className="wfmain">
                    <a href={href.workflow(name, w.name)} className="wfname mono" onClick={e => e.stopPropagation()}>{w.name}</a>
                    <div className="wfdesc" title={w.description}>{w.description || <span className="muted">no description</span>}</div>
                  </div>
                  <div className="wfmeta">
                    <span>{w.steps?.length || 0} step{w.steps?.length === 1 ? '' : 's'}</span>
                    {/* A workflow is the YAML AND what sits beside it; a list
                        that hid that would look reusable and not be. */}
                    {!!w.files?.length && (
                      <span title={w.files.map(f => f.path).join('\n')}>
                        + {w.files.length} file{w.files.length === 1 ? '' : 's'}
                      </span>)}
                    {triggerNames(w).filter(t => t !== 'workflow_dispatch').map(t => (
                      <span key={t} className="pill">{t.replace('workflow_', '')}</span>))}
                  </div>
                  <div className="wflast">
                    {last
                      ? <><span className={`badge ${last.status}`}>{last.status.replace(/_/g, ' ')}</span>
                        <span className="muted">{ago(last.createdAt)}</span></>
                      : <span className="muted">never run</span>}
                  </div>
                  <span onClick={e => e.stopPropagation()}>
                    {canDispatch(w)
                      ? <a href={href.startRun(name, w.name)}><button className="small">Run</button></a>
                      : <button className="ghost small" disabled title={`${w.name} is started by ${triggerNames(w).join(', ')}, not by hand`}>Run</button>}
                  </span>
                </li>)
            })}
          </ul>
        </div>)}

      {tab === 'runs' && (
        <div className="card">
          <DataTable
            rows={runs} columns={runColumns} filters={runFilters}
            getKey={r => r.id}
            onRowClick={r => { location.hash = href.run(name, shortName(r.workflow), r.id) }}
            rowClass={r => (r.status === 'failed' ? 'bad' : '')}
            initialSort={{ key: 'created', dir: 'desc' }}
            searchPlaceholder="Search runs…"
            empty="Nothing has run here yet." />
        </div>)}

      {/* This repository's own environment — a token that can push here has no
          business reaching a workflow from another project — and what the
          project remembers between runs. */}
      {tab === 'settings' && (
        <>
          <EnvStore project={name} />
          <StateStore project={name} />
        </>)}
    </>
  )
}
