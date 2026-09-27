import { useCallback, useEffect, useMemo, useState } from 'react'
import StateStore from '../components/StateStore'
import Crumbs, { Tabs } from '../components/Crumbs'
import { api, type Run, type Workflow } from '../api'
import DataTable, { type Column, type Filter } from '../components/DataTable'
import FileList from '../components/FileList'
import StepHarness from '../components/StepHarness'
import WorkflowCanvas from '../components/WorkflowCanvas'
import { ago } from '../lib/time'
import { href } from '../lib/routes'
import { canDispatch, clip, sameWorkflow, shapeOf, summariseRun, triggerNames } from '../lib/workflow'

type Tab = 'runs' | 'steps' | 'memory'

/** One workflow, opened: what it has done (Runs), what it is (Steps), and what
 *  it remembers between runs (Memory). Editing opens the builder on this same
 *  workflow, and Run starts one — both from here, because this is where a
 *  person already is when they decide to do either. */
export default function WorkflowPage({ project, name }: { project: string; name: string }) {
  const [def, setDef] = useState<Workflow>()
  const [runs, setRuns] = useState<Run[]>()
  const [err, setErr] = useState('')
  // Chosen once the runs arrive: a workflow that has run opens on its runs, one
  // that never has opens on its steps — there is nothing else to look at yet.
  const [tab, setTab] = useState<Tab>()

  const load = useCallback(() => {
    Promise.all([api.workflows(), api.runs({ project, workflow: name })])
      .then(([ws, r]) => {
        const d = ws.find(w => (w.source || 'local') === project && sameWorkflow(w.name, name))
        if (!d) { setErr(`No workflow named ${name} in ${project}.`); return }
        setDef(d); setRuns(r); setErr('')
        setTab(t => t ?? (r.length ? 'runs' : 'steps'))
      })
      .catch(e => setErr(e instanceof Error ? e.message : String(e)))
  }, [project, name])
  useEffect(() => {
    load()
    const t = setInterval(load, 5000)
    return () => clearInterval(t)
  }, [load])

  const filters: Filter<Run>[] = useMemo(() => [
    { key: 'status', label: 'statuses', options: [...new Set((runs || []).map(r => r.status))].sort(), match: (r, v) => r.status === v },
  ], [runs])

  const crumbs = <Crumbs items={[['Projects', href.projects()], [project, href.project(project)]]} />
  if (err) return <>{crumbs}<div className="banner err">{err}</div></>
  if (!def || !runs || !tab) return <>{crumbs}<div className="muted">loading…</div></>

  const columns: Column<Run>[] = [
    {
      key: 'status', header: 'Status', width: 130,
      value: r => r.status,
      cell: r => <span className={`badge ${r.status}`}>{r.status.replace(/_/g, ' ')}</span>,
    },
    {
      key: 'what', header: 'What it is about',
      value: r => summariseRun(r),
      cell: r => {
        const s = summariseRun(r)
        return s ? <span title={s}>{clip(s, 90)}</span> : <span className="muted mono">{r.id.slice(0, 8)}</span>
      },
    },
    {
      key: 'step', header: 'Step', width: 160,
      value: r => r.currentStep || '',
      cell: r => r.currentStep ? <span className="mono">{r.currentStep}</span> : <span className="muted">—</span>,
    },
    {
      key: 'created', header: 'Started', width: 110,
      value: r => r.createdAt,
      cell: r => <span className="muted" title={new Date(r.createdAt).toLocaleString()}>{ago(r.createdAt)}</span>,
    },
    {
      key: 'id', header: 'Run', width: 100, value: r => r.id,
      cell: r => <span className="mono muted">{r.id.slice(0, 8)}</span>,
    },
  ]

  return (
    <>
      {crumbs}
      <div className="head">
        <div style={{ minWidth: 0 }}>
          <h1 className="mono">{def.name}</h1>
          {def.description && <p>{def.description}</p>}
        </div>
        <div style={{ display: 'flex', gap: 8 }}>
          <a href={href.edit(project, def.name)}><button className="ghost">Edit steps</button></a>
          {canDispatch(def)
            ? <a href={href.startRun(project, def.name)}><button>Run</button></a>
            // Not merely disabled: saying WHY is the difference between a dead
            // button and an explanation.
            : <button disabled title={`${def.name} is started by ${triggerNames(def).join(', ')}, not by hand`}>Run</button>}
        </div>
      </div>

      <Tabs<Tab> value={tab} onChange={setTab} tabs={[
        { key: 'runs', label: 'Runs', count: runs.length },
        { key: 'steps', label: 'Steps', count: def.steps.length },
        { key: 'memory', label: 'Memory' },
      ]} />

      {tab === 'runs' && (
        <div className="card">
          <DataTable
            rows={runs} columns={columns} filters={filters}
            getKey={r => r.id}
            onRowClick={r => { location.hash = href.run(project, def.name, r.id) }}
            rowClass={r => (r.status === 'failed' ? 'bad' : '')}
            initialSort={{ key: 'created', dir: 'desc' }}
            searchPlaceholder="Search runs…"
            empty={canDispatch(def)
              ? <>Never run. <a href={href.startRun(project, def.name)}>Start the first run</a>.</>
              : `Never run. It starts on ${triggerNames(def).join(', ')}.`} />
        </div>)}

      {/* What the workflow IS: how it starts, its shape, every step as the
          agent it becomes, and every file that travels with it. */}
      {tab === 'steps' && (
        <div className="card">
          <div className="subhead">
            <span style={{ display: 'inline-flex', gap: 4, flexWrap: 'wrap' }}>
              {triggerNames(def).map(t => <span key={t} className="pill">{t.replace('workflow_', '')}</span>)}
              <span className="pill">{shapeOf(def)}</span>
            </span>
            <span className="muted mono" style={{ fontSize: 12 }}>{def.path}</span>
          </div>
          {def.steps.length > 1 && <WorkflowCanvas steps={def.steps} />}
          <div style={{ marginTop: 14 }}>
            {def.steps.map((s, i) => <StepHarness key={s.id} step={s} index={i} />)}
          </div>
          {/* Every file, a README as much as a script: copy this workflow and
              these are what come with it. */}
          <div style={{ marginTop: 14 }}>
            <h3>Files that travel with it</h3>
            <FileList files={def.files} empty="just the YAML" />
          </div>
        </div>)}

      {/* The answer to "why does the scheduled run think it is already up to
          date?" — on the workflow's page instead of in a database client. */}
      {tab === 'memory' && <StateStore workflow={def.name} />}
    </>
  )
}
