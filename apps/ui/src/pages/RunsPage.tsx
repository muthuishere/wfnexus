import { useEffect, useState } from 'react'
import { api, type Run } from '../api'
import DataTable, { type Column, type Filter } from '../components/DataTable'
import { ago } from '../lib/time'

export default function RunsPage() {
  const [runs, setRuns] = useState<Run[]>([])
  const [err, setErr] = useState('')

  useEffect(() => {
    const load = () => api.runs().then(r => { setRuns(r); setErr('') })
      .catch(e => setErr(e instanceof Error ? e.message : String(e)))
    load()
    const t = setInterval(load, 4000)
    return () => clearInterval(t)
  }, [])

  const columns: Column<Run>[] = [
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
      key: 'what', header: 'What it is about',
      // A run's input has no fixed shape, so the most human field present is
      // shown and the id is the fallback — an id column alone makes the list
      // unreadable at a glance, which is the only thing a list is for.
      value: r => summarise(r),
      cell: r => {
        const s = summarise(r)
        return s ? <span title={s}>{clip(s, 90)}</span> : <span className="muted mono">{r.id.slice(0, 8)}</span>
      },
    },
    {
      key: 'step', header: 'Step', width: 150,
      value: r => r.currentStep || '',
      cell: r => r.currentStep ? <span className="mono">{r.currentStep}</span> : <span className="muted">—</span>,
    },
    {
      key: 'created', header: 'Started', width: 120,
      // Sorted on the raw timestamp, shown as "3m ago": the reader wants
      // recency, and doing that arithmetic is not their job.
      value: r => r.createdAt,
      cell: r => <span className="muted" title={new Date(r.createdAt).toLocaleString()}>{ago(r.createdAt)}</span>,
    },
  ]

  const filters: Filter<Run>[] = [
    {
      key: 'status', label: 'statuses',
      options: [...new Set(runs.map(r => r.status))].sort(),
      match: (r, v) => r.status === v,
    },
    {
      key: 'workflow', label: 'workflows',
      options: [...new Set(runs.map(r => r.workflow))].sort(),
      match: (r, v) => r.workflow === v,
    },
  ]

  return (
    <>
      <div className="head">
        <div>
          <h1>Runs</h1>
          <p>Every execution, newest first. A run is a workflow over one repository.</p>
        </div>
        <a href="#/workflows"><button>New run</button></a>
      </div>
      {err && <div className="banner err">{err}</div>}
      <div className="card">
        <DataTable
          rows={runs}
          columns={columns}
          filters={filters}
          getKey={r => r.id}
          onRowClick={r => { location.hash = `#/runs/${r.id}` }}
          rowClass={r => (r.status === 'failed' ? 'bad' : '')}
          initialSort={{ key: 'created', dir: 'desc' }}
          searchPlaceholder="Search runs…"
          empty="No runs yet. Start one from a workflow."
        />
      </div>
    </>
  )
}

/** summarise picks the most human field a run's input happens to carry. */
function summarise(r: Run): string {
  const i = r.input || {}
  for (const k of ['title', 'report', 'claim', 'question', 'issue_title', 'task', 'target', 'repo_path', 'repo_url']) {
    const v = i[k]
    if (typeof v === 'string' && v.trim()) return v.trim()
  }
  return r.error || ''
}

function clip(s: string, n: number) { return s.length > n ? s.slice(0, n) + '…' : s }
