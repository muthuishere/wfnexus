import { useCallback, useEffect, useState } from 'react'
import { api, type Pool, type Worker } from '../api'
import DataTable, { type Column } from '../components/DataTable'
import { ago } from '../lib/time'

/** The machines that have joined, and the one command that adds another.
 *
 *  A workflow says `runs-on: windows`. It never says which machine. A machine
 *  joins by running one line here — the same shape as a GitHub self-hosted
 *  runner or a Jenkins node — and from then on it polls for work carrying its
 *  labels. Nothing ever connects *to* it, which is why the platform can be a
 *  pod behind an ingress and the machine can be a laptop behind NAT. */
export default function WorkersPage() {
  const [pool, setPool] = useState<Pool>()
  const [err, setErr] = useState('')
  const [note, setNote] = useState('')

  const load = useCallback(() => {
    api.workers().then(p => { setPool(p); setErr('') })
      .catch(e => setErr(e instanceof Error ? e.message : String(e)))
  }, [])
  useEffect(() => {
    load()
    // A worker is online because it is polling, so the list is only true if it
    // keeps being asked.
    const t = setInterval(load, 10_000)
    return () => clearInterval(t)
  }, [load])

  const columns: Column<Worker>[] = [
    {
      key: 'name', header: 'Machine', width: 200,
      value: w => w.name,
      cell: w => <span className="mono" style={{ fontWeight: 550 }}>{w.name}</span>,
    },
    {
      key: 'status', header: 'Status', width: 110,
      value: w => w.status,
      cell: w => <span className={`badge ${w.status === 'online' ? 'done' : 'failed'}`}>{w.status}</span>,
    },
    {
      key: 'labels', header: 'Labels it serves', width: 260,
      value: w => w.labels.join(','),
      cell: w => w.labels.map(l => <span key={l} className="pill" style={{ marginRight: 4 }}>{l}</span>),
    },
    {
      key: 'platform', header: 'Platform', width: 150,
      value: w => `${w.os}/${w.arch}`,
      cell: w => <span className="mono muted">{w.os}/{w.arch}</span>,
    },
    {
      key: 'seen', header: 'Last seen', width: 130,
      value: w => w.lastSeen,
      cell: w => <span className="muted">{ago(w.lastSeen)}</span>,
    },
    {
      key: 'actions', header: '', width: 90, align: 'right', sortable: false,
      cell: w => (
        <button className="ghost small danger-text" onClick={e => {
          e.stopPropagation()
          if (!confirm(`Remove ${w.name}? It stops being sent work and can join again with the command below.`)) return
          api.removeWorker(w.id).then(() => { setNote(`Removed ${w.name}.`); load() })
            .catch(er => setErr(String(er.message || er)))
        }}>Remove</button>),
    },
  ]

  return (
    <>
      <div className="head">
        <div>
          <h1>Workers</h1>
          <p>
            A workflow says <span className="mono">runs-on: windows</span> — a label, never a machine.
            Machines that have joined take the steps whose label they hold, with the toolchain
            installed <em>there</em>. They connect out, so nothing has to reach in.
          </p>
        </div>
      </div>

      {err && <div className="banner err">{err}</div>}
      {note && <div className="banner ok">{note}</div>}

      <div className="card">
        {!pool ? <div className="muted">loading…</div> : (
          <DataTable
            rows={pool.workers} columns={columns} getKey={w => w.id}
            initialSort={{ key: 'name', dir: 'asc' }}
            searchPlaceholder="Search machines…"
            empty="No machines have joined. Everything runs on the platform itself — which is fine until a step needs a toolchain this box does not have." />)}
      </div>

      {pool && (
        <div className="card">
          <div className="subhead"><h2>Add a machine</h2></div>
          <p className="muted" style={{ marginBottom: 10 }}>
            Run this on the machine you want — a build VM, a Windows box, a Mac on someone's desk.
            Change <span className="mono">--labels</span> to what that machine actually offers, then
            point a job at it with <span className="mono">runs-on:</span>.
          </p>
          <Copyable text={pool.joinCommand} />
          <p className="muted" style={{ marginTop: 10 }}>
            Then <span className="mono">wfx-runner run</span> — or install it as a service; see{' '}
            <span className="mono">infra/README.md</span>.
          </p>
          <p className="muted" style={{ marginTop: 14 }}>
            Served by the platform itself, needing no machine:{' '}
            {pool.localLabels.map(l => <span key={l} className="pill" style={{ marginRight: 4 }}>{l}</span>)}
          </p>
          <button className="ghost small" style={{ marginTop: 14 }} onClick={() => {
            if (!confirm('Rotate the join token? The command above stops working. Machines already joined hold their own token and keep working.')) return
            api.rotateWorkerToken().then(() => { setNote('Join token rotated.'); load() })
              .catch(e => setErr(String(e.message || e)))
          }}>Rotate join token</button>
        </div>)}
    </>
  )
}

/** The join command is meant to be pasted somewhere else, so copying it is the
 *  primary action rather than something to select by hand. */
function Copyable({ text }: { text: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <div style={{ display: 'flex', gap: 8, alignItems: 'stretch' }}>
      <code className="mono" style={{
        flex: 1, padding: '10px 12px', overflowX: 'auto', whiteSpace: 'pre',
        border: '1px solid var(--line, #ddd)', borderRadius: 6,
      }}>{text}</code>
      <button onClick={() => {
        navigator.clipboard?.writeText(text).then(() => {
          setCopied(true); setTimeout(() => setCopied(false), 1500)
        })
      }}>{copied ? 'Copied' : 'Copy'}</button>
    </div>
  )
}
