import { useEffect, useMemo, useState } from 'react'
import { api, type Workflow } from '../api'
import DataTable, { type Column, type Filter } from '../components/DataTable'
import StepHarness from '../components/StepHarness'
import WorkflowCanvas from '../components/WorkflowCanvas'

export default function WorkflowsPage() {
  const [wfs, setWfs] = useState<Workflow[]>()
  const [err, setErr] = useState('')
  const [open, setOpen] = useState<string>()

  const load = () => api.workflows().then(w => { setWfs(w); setErr('') })
    .catch(e => setErr(e instanceof Error ? e.message : String(e)))
  useEffect(() => { load() }, [])
  const reload = () => api.reload().then(w => { setWfs(w); setErr('') })
    .catch(e => setErr(e instanceof Error ? e.message : String(e)))

  // `wfs || []` is a new array every render, so memoising against it memoises
  // nothing; the memo depends on the state itself.
  const rows = useMemo(() => wfs || [], [wfs])
  const detail = useMemo(() => rows.find(w => w.name === open), [rows, open])

  const columns: Column<Workflow>[] = [
    {
      key: 'name', header: 'Workflow', width: 200,
      value: w => w.name,
      cell: w => <span className="mono" style={{ fontWeight: 500 }}>{w.name}</span>,
    },
    {
      key: 'triggers', header: 'Starts on', width: 190,
      // What may start a workflow is the first thing worth knowing about it:
      // a schedule-only workflow has no "New run" button for a reason.
      value: w => triggerNames(w).join(' '),
      cell: w => <>{triggerNames(w).map(t => (
        <span key={t} className="pill" style={{ marginRight: 4 }}>{t.replace('workflow_', '')}</span>))}</>,
    },
    {
      key: 'shape', header: 'Shape', width: 120,
      value: w => shapeOf(w),
      cell: w => <span className="muted">{shapeOf(w)}</span>,
    },
    {
      key: 'steps', header: 'Steps', width: 70, align: 'right',
      value: w => w.steps?.length || 0,
      cell: w => w.steps?.length || 0,
    },
    {
      key: 'description', header: 'What it does',
      value: w => w.description || '',
      cell: w => <span title={w.description}>{clip(w.description || '', 110)}</span>,
    },
    {
      key: 'actions', header: '', sortable: false, width: 190, align: 'right',
      cell: w => (
        <span onClick={e => e.stopPropagation()} style={{ display: 'inline-flex', gap: 5 }}>
          <a href={`#/workflows/${encodeURIComponent(w.name)}/edit`}><button className="ghost small">Edit</button></a>
          {canDispatch(w)
            ? <a href={`#/workflows/${encodeURIComponent(w.name)}/new`}><button className="small">Run</button></a>
            // Not merely disabled: saying WHY is the difference between a dead
            // button and an explanation.
            : <button className="ghost small" disabled title={`${w.name} is started by ${triggerNames(w).join(', ')}, not by hand`}>Run</button>}
        </span>),
    },
  ]

  const filters: Filter<Workflow>[] = [
    {
      key: 'trigger', label: 'triggers',
      options: [...new Set(rows.flatMap(triggerNames))].sort(),
      match: (w, v) => triggerNames(w).includes(v),
    },
    {
      key: 'source', label: 'sources',
      options: [...new Set(rows.map(w => w.source || 'local'))].sort(),
      match: (w, v) => (w.source || 'local') === v,
    },
    {
      key: 'shape', label: 'shapes',
      options: ['sequential', 'parallel', 'goal-planned'],
      match: (w, v) => shapeOf(w) === v,
    },
  ]

  return (
    <>
      <div className="head">
        <div>
          <h1>Workflows</h1>
          <p>Each step is a whole agent — its own soul, skills, tools, team, budget and output contract.</p>
        </div>
        <div style={{ display: 'flex', gap: 8 }}>
          <button className="ghost" onClick={reload}>Reload from disk</button>
          <a href="#/workflows/new"><button>New workflow</button></a>
        </div>
      </div>

      {err && <div className="banner err">{err}</div>}
      {!wfs && !err && <div className="card muted">loading…</div>}

      {wfs && (
        <div className="card">
          <DataTable
            rows={rows}
            columns={columns}
            filters={filters}
            getKey={w => w.name}
            onRowClick={w => setOpen(open === w.name ? undefined : w.name)}
            initialSort={{ key: 'name', dir: 'asc' }}
            searchPlaceholder="Search workflows…"
            empty={<>No workflows loaded. Drop a YAML file in <span className="mono">workflows/</span>, or <span className="mono">wfx import</span> a repository.</>}
          />
        </div>)}

      {detail && (
        <div className="card">
          <div className="subhead">
            <h2>{detail.name}</h2>
            <span className="muted mono" style={{ fontSize: 12 }}>{detail.path}</span>
            <button className="ghost small" onClick={() => setOpen(undefined)}>Close</button>
          </div>
          <p className="muted" style={{ marginBottom: 12 }}>{detail.description}</p>
          {detail.steps.length > 1 && <WorkflowCanvas steps={detail.steps} />}
          <div style={{ marginTop: 14 }}>
            {detail.steps.map((s, i) => <StepHarness key={s.id} step={s} index={i} />)}
          </div>
        </div>)}
    </>
  )
}

function triggerNames(w: Workflow): string[] {
  const on = w.on
  if (!on) return ['workflow_dispatch']
  const out: string[] = []
  if (on.dispatch) out.push('workflow_dispatch')
  if (on.schedule?.length) out.push('schedule')
  if (on.repositoryDispatch) out.push('repository_dispatch')
  if (on.workflowCall) out.push('workflow_call')
  return out.length ? out : ['workflow_dispatch']
}

function canDispatch(w: Workflow): boolean { return triggerNames(w).includes('workflow_dispatch') }

function shapeOf(w: Workflow): string {
  if (w.goal) return 'goal-planned'
  if (w.steps?.some(s => s.needs?.length)) return 'parallel'
  return 'sequential'
}

function clip(s: string, n: number) { return s.length > n ? s.slice(0, n) + '…' : s }
