import { useCallback, useEffect, useMemo, useState } from 'react'
import EnvStore from '../components/EnvStore'
import { api, type Project, type Run, type Workflow } from '../api'
import DataTable, { type Column, type Filter } from '../components/DataTable'
import { ago } from '../lib/time'

/** One project: its workflows, and its runs. The middle of
 *  project → workflow → runs, and the view that answers "what happens in this
 *  repository, and did the last one work". */
export default function ProjectPage({ name, workflow }: { name: string; workflow?: string }) {
  const [project, setProject] = useState<Project>()
  const [workflows, setWorkflows] = useState<Workflow[]>([])
  const [runs, setRuns] = useState<Run[]>([])
  const [err, setErr] = useState('')

  const load = useCallback(() => {
    Promise.all([api.project(name), api.workflows(), api.runs({ project: name, workflow })])
      .then(([p, w, r]) => { setProject(p); setWorkflows(w); setRuns(r); setErr('') })
      .catch(e => setErr(e instanceof Error ? e.message : String(e)))
  }, [name, workflow])

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

  if (err) return <div className="banner err">{err}</div>
  if (!project) return <div className="muted">loading…</div>

  const wfColumns: Column<Workflow>[] = [
    {
      key: 'name', header: 'Workflow', width: 210,
      value: w => w.name,
      cell: w => <span className="mono" style={{ fontWeight: 550 }}>{w.name}</span>,
    },
    {
      key: 'runs', header: 'Runs', width: 80, align: 'right',
      value: w => runs.filter(r => sameWorkflow(r.workflow, w.name)).length,
      cell: w => {
        const n = runs.filter(r => sameWorkflow(r.workflow, w.name)).length
        return n ? <a href={`#/projects/${encodeURIComponent(name)}/${encodeURIComponent(w.name)}`}>{n}</a>
          : <span className="muted">—</span>
      },
    },
    {
      key: 'last', header: 'Last run', width: 160,
      value: w => runs.find(r => sameWorkflow(r.workflow, w.name))?.createdAt || '',
      cell: w => {
        const r = runs.find(x => sameWorkflow(x.workflow, w.name))
        return r
          ? <><span className={`badge ${r.status}`}>{r.status.replace(/_/g, ' ')}</span>{' '}
            <span className="muted" style={{ fontSize: 12 }}>{ago(r.createdAt)}</span></>
          : <span className="muted">never</span>
      },
    },
    {
      key: 'description', header: 'What it does',
      value: w => w.description || '',
      cell: w => <span title={w.description}>{clip(w.description || '', 90)}</span>,
    },
    {
      key: 'actions', header: '', width: 120, align: 'right', sortable: false,
      cell: w => (
        <span onClick={e => e.stopPropagation()} style={{ display: 'inline-flex', gap: 5 }}>
          <a href={`#/workflows/${encodeURIComponent(w.name)}/edit`}><button className="ghost small">Edit</button></a>
          <a href={`#/workflows/${encodeURIComponent(w.name)}/new`}><button className="small">Run</button></a>
        </span>),
    },
  ]

  const runColumns: Column<Run>[] = [
    {
      key: 'status', header: 'Status', width: 130,
      value: r => r.status,
      cell: r => <span className={`badge ${r.status}`}>{r.status.replace(/_/g, ' ')}</span>,
    },
    {
      key: 'workflow', header: 'Workflow', width: 190,
      value: r => r.workflow,
      cell: r => <span className="mono">{r.workflow}</span>,
    },
    {
      key: 'step', header: 'Step', width: 150,
      value: r => r.currentStep || '',
      cell: r => r.currentStep ? <span className="mono">{r.currentStep}</span> : <span className="muted">—</span>,
    },
    {
      key: 'created', header: 'Started', width: 120,
      value: r => r.createdAt,
      cell: r => <span className="muted" title={new Date(r.createdAt).toLocaleString()}>{ago(r.createdAt)}</span>,
    },
    {
      key: 'id', header: 'Run', value: r => r.id,
      cell: r => <span className="mono muted">{r.id.slice(0, 8)}</span>,
    },
  ]

  const runFilters: Filter<Run>[] = [
    { key: 'status', label: 'statuses', options: [...new Set(runs.map(r => r.status))].sort(), match: (r, v) => r.status === v },
    { key: 'workflow', label: 'workflows', options: [...new Set(runs.map(r => r.workflow))].sort(), match: (r, v) => r.workflow === v },
  ]

  return (
    <>
      <div className="head">
        <div style={{ minWidth: 0 }}>
          <div className="muted" style={{ fontSize: 12.5, marginBottom: 2 }}>
            <a href="#/projects">Projects</a>
            {workflow && <> / <a href={`#/projects/${encodeURIComponent(name)}`}>{name}</a></>}
          </div>
          <h1>{workflow || project.name}</h1>
          <p className="mono">{project.url || project.repo || project.dir}</p>
        </div>
        {!workflow && <a href="#/workflows/new"><button>New workflow</button></a>}
      </div>

      {!!project.problems?.length && (
        <div className="banner err">
          <strong>{project.problems.length} workflow(s) in this project did not load</strong>
          <ul style={{ margin: '6px 0 0', paddingLeft: 18 }}>
            {project.problems.map((p, i) => <li key={i}><span className="mono">{p.location}</span> — {p.reason}</li>)}
          </ul>
        </div>)}

      {!workflow && (
        <div className="card">
          <div className="subhead"><h2>Workflows</h2><span className="muted" style={{ fontSize: 12.5 }}>{mine.length}</span></div>
          <DataTable
            rows={mine} columns={wfColumns} getKey={w => w.name}
            onRowClick={w => { location.hash = `#/projects/${encodeURIComponent(name)}/${encodeURIComponent(w.name)}` }}
            initialSort={{ key: 'last', dir: 'desc' }}
            searchPlaceholder="Search workflows…"
            empty={<>No workflows. Add YAML files to <span className="mono">.wfx/workflows/</span> in this repository.</>} />
        </div>)}

      {/* This repository's own environment — a token that can push here has no
          business reaching a workflow from another project. Shown only on the
          project itself, not when drilled into one of its workflows. */}
      {!workflow && <EnvStore project={name} />}

      <div className="card">
        <div className="subhead">
          <h2>{workflow ? 'Runs of this workflow' : 'Recent runs'}</h2>
          <span className="muted" style={{ fontSize: 12.5 }}>{project.runs} in total</span>
        </div>
        <DataTable
          rows={runs} columns={runColumns} filters={workflow ? [runFilters[0]] : runFilters}
          getKey={r => r.id}
          onRowClick={r => { location.hash = `#/runs/${r.id}` }}
          rowClass={r => (r.status === 'failed' ? 'bad' : '')}
          initialSort={{ key: 'created', dir: 'desc' }}
          searchPlaceholder="Search runs…"
          empty="Nothing has run here yet." />
      </div>
    </>
  )
}

/** A workflow is reachable by its short name and as `project/name`, so a run
 *  recorded under either spelling belongs to the same workflow. */
function sameWorkflow(runWorkflow: string, name: string): boolean {
  if (runWorkflow === name) return true
  const tail = (s: string) => (s.includes('/') ? s.slice(s.indexOf('/') + 1) : s)
  return tail(runWorkflow) === tail(name)
}

function clip(s: string, n: number) { return s.length > n ? s.slice(0, n) + '…' : s }
