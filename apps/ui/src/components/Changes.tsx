import { useCallback, useEffect, useState } from 'react'
import { lifecycle, prLabel, type DriftEntry, type Proposal, type ProposalDetail, type Review } from '../lifecycle'

const msg = (e: unknown) => (e instanceof Error ? e.message : String(e))

/** The classifier's verdict on a proposal. "unreviewed" is said plainly
 *  rather than shown as a neutral badge that looks like a pass. */
export function ReviewBadge({ review }: { review?: Review }) {
  const v = review?.verdict || 'unreviewed'
  const cls = v === 'approve' ? 'succeeded' : v === 'reject' ? 'failed' : 'queued'
  const label = v === 'approve' ? 'classifier: approve' : v === 'reject' ? 'classifier: reject' : 'not reviewed'
  const score = v !== 'unreviewed' && typeof review?.score === 'number' ? ` · ${Math.round(review.score * 100) / 100}` : ''
  return <span className={`badge ${cls}`} title={review?.reason || ''} data-testid="review-badge">{label}{score}</span>
}

export function StatusBadge({ status }: { status: Proposal['status'] }) {
  const cls = status === 'merged' ? 'succeeded' : status === 'rejected' ? 'failed' : status === 'approved' ? 'running' : 'awaiting_approval'
  return <span className={`badge ${cls}`}>{status}</span>
}

/** "Proposed change: PR #n" — what a save or delete now produces. */
export function ProposedNotice({ proposal }: { proposal: Proposal }) {
  return (
    <div className="banner ok" role="status">
      Proposed change: {proposal.pr_url
        ? <a href={proposal.pr_url} target="_blank" rel="noreferrer">{prLabel(proposal)}</a>
        : <span className="mono">{prLabel(proposal)}</span>}
      {' '}({proposal.kind} <span className="mono">{proposal.workflow}</span>) — not live until it is approved and merged.
    </div>
  )
}

/** Proposed changes to a project's workflows, optionally narrowed to one
 *  workflow. A list, and one proposal opened below it with its diff. */
export default function Changes({ project, workflow }: { project: string; workflow?: string }) {
  const [list, setList] = useState<Proposal[]>()
  const [err, setErr] = useState('')
  const [open, setOpen] = useState<string>()

  const load = useCallback(() => {
    lifecycle.proposals(project)
      .then(ps => { setList(workflow ? ps.filter(p => p.workflow === workflow) : ps); setErr('') })
      .catch(e => setErr(msg(e)))
  }, [project, workflow])
  useEffect(load, [load])

  return (
    <>
      {!workflow && <Drift project={project} onProposed={load} />}
      <div className="card flush">
        <div className="cardhead"><h2>Proposed changes</h2><span className="muted">{list?.length ?? ''}</span></div>
        {err && <div className="banner err" style={{ margin: 12 }}>{err}</div>}
        {!list && !err && <div className="empty">loading…</div>}
        {list?.length === 0 && <div className="empty">No proposed changes. Saving or deleting a workflow opens one.</div>}
        {!!list?.length && (
          <table className="rows proposals"><thead><tr>
            <th>Change</th><th>Workflow</th><th>Branch / PR</th><th>Review</th><th>Status</th>
          </tr></thead><tbody>
            {list.map(p => (
              <tr key={p.id} className={open === p.id ? 'sel' : ''} onClick={() => setOpen(o => (o === p.id ? undefined : p.id))}>
                <td><span className="pill">{p.kind}</span></td>
                <td className="mono">{p.workflow}</td>
                <td className="mono">{p.pr_url
                  ? <a href={p.pr_url} target="_blank" rel="noreferrer" onClick={e => e.stopPropagation()}>{prLabel(p)}</a>
                  : p.branch}</td>
                <td><ReviewBadge review={p.review} /></td>
                <td><StatusBadge status={p.status} /></td>
              </tr>))}
          </tbody></table>)}
      </div>
      {open && <ProposalView id={open} onChanged={load} />}
    </>
  )
}

/** One proposal: what the classifier said, the diff, and the decision. */
export function ProposalView({ id, onChanged }: { id: string; onChanged?: () => void }) {
  const [p, setP] = useState<ProposalDetail>()
  const [err, setErr] = useState('')
  const [rejecting, setRejecting] = useState(false)
  const [reason, setReason] = useState('')
  const [busy, setBusy] = useState(false)

  const load = useCallback(() => {
    lifecycle.proposal(id).then(d => { setP(d); setErr('') }).catch(e => setErr(msg(e)))
  }, [id])
  useEffect(load, [load])

  const act = (f: () => Promise<unknown>) => {
    setBusy(true); setErr('')
    f().then(() => { setRejecting(false); setReason(''); load(); onChanged?.() })
      .catch(e => setErr(msg(e))).finally(() => setBusy(false))
  }

  if (err && !p) return <div className="banner err">{err}</div>
  if (!p) return <div className="card muted">loading…</div>
  const decided = p.status !== 'pending'
  return (
    <div className="card proposal" aria-label="Proposal">
      <div className="subhead">
        <h2><span className="pill">{p.kind}</span> <span className="mono">{p.workflow}</span></h2>
        <StatusBadge status={p.status} />
        <div style={{ display: 'flex', gap: 8 }}>
          <button disabled={decided || busy} onClick={() => act(() => lifecycle.approve(p.id))}>Approve</button>
          <button className="ghost danger-text" disabled={decided || busy} onClick={() => setRejecting(r => !r)}>Reject</button>
        </div>
      </div>
      <div className="reviewline">
        <ReviewBadge review={p.review} />
        {p.review?.reason && <span className="muted">{p.review.reason}</span>}
      </div>
      <div className="muted mono" style={{ fontSize: 12, margin: '8px 0' }}>
        branch {p.branch}{p.pr_url && <> · <a href={p.pr_url} target="_blank" rel="noreferrer">{prLabel(p)}</a></>}
      </div>
      {rejecting && (
        <div className="rejectbox">
          <label htmlFor={`reason-${p.id}`}>Why reject it?</label>
          <input id={`reason-${p.id}`} value={reason} onChange={e => setReason(e.target.value)} placeholder="the reason is recorded on the proposal" />
          <div className="actions">
            <button className="danger" disabled={!reason.trim() || busy} onClick={() => act(() => lifecycle.reject(p.id, reason.trim()))}>Reject change</button>
          </div>
        </div>)}
      {err && <div className="banner err">{err}</div>}
      <Diff text={p.diff} />
    </div>
  )
}

/** A unified diff, coloured by line. */
export function Diff({ text }: { text: string }) {
  if (!text) return <div className="empty">No diff.</div>
  return (
    <pre className="diff">{text.split('\n').map((l, i) => {
      const c = l.startsWith('+++') || l.startsWith('---') ? 'meta'
        : l.startsWith('@@') ? 'hunk' : l.startsWith('+') ? 'add' : l.startsWith('-') ? 'del' : ''
      return <div key={i} className={c}>{l || ' '}</div>
    })}</pre>
  )
}

/** Workflows on disk that no commit tracks: edited in place, never proposed.
 *  One button turns them into a proposal like any other change. */
export function Drift({ project, onProposed }: { project: string; onProposed?: () => void }) {
  const [rows, setRows] = useState<DriftEntry[]>()
  const [err, setErr] = useState('')
  const [done, setDone] = useState<Proposal>()
  const [busy, setBusy] = useState(false)

  const load = useCallback(() => {
    lifecycle.drift(project).then(r => { setRows(r); setErr('') }).catch(e => setErr(msg(e)))
  }, [project])
  useEffect(load, [load])

  if (!rows?.length && !err && !done) return null
  return (
    <div className="card drift">
      <div className="subhead">
        <h2>Untracked workflows</h2>
        {!!rows?.length && (
          <button disabled={busy} onClick={() => {
            setBusy(true)
            lifecycle.proposeDrift(project).then(p => { setDone(p); load(); onProposed?.() })
              .catch(e => setErr(msg(e))).finally(() => setBusy(false))
          }}>Propose commit</button>)}
      </div>
      {err && <div className="banner err">{err}</div>}
      {done && <ProposedNotice proposal={done} />}
      {!!rows?.length && (
        <>
          <p className="muted" style={{ marginBottom: 8 }}>On disk, but in no commit — nobody has reviewed them.</p>
          <ul className="driftlist">
            {rows.map(r => <li key={r.path}><span className="mono">{r.workflow}</span> <span className="muted mono">{r.path}</span>{r.status && <span className="pill">{r.status}</span>}</li>)}
          </ul>
        </>)}
    </div>
  )
}
