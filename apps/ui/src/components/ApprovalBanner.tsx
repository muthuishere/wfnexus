import { useState } from 'react'
import type { Step } from '../api'

/** The first of the human's two buttons: the engine halted BEFORE this step ran —
 *  the model has not been called yet, so approving is what authorises the spend. */
export default function ApprovalBanner({ step, stepId, onApprove, onReject }: {
  step?: Step; stepId: string
  onApprove: () => void; onReject: (reason: string) => void
}) {
  const [rejecting, setRejecting] = useState(false)
  const [reason, setReason] = useState('')
  return (
    <div className="banner warn hitl">
      <div className="hitlhead">
        <span className="badge awaiting_approval">awaiting approval</span>
        <b>{step?.name || stepId}</b>
        <span className="muted mono">{stepId}</span>
      </div>
      <p>
        The run is halted <b>before</b> this step — no model has been called for it yet.
        {step?.description ? <> {step.description}</> : null}
      </p>
      {(step?.tools?.length || step?.mcp?.length) ? (
        <div className="chips">
          {(step?.tools || []).map(t => <span key={t}>{t}</span>)}
          {(step?.mcp || []).map(m => <span key={m}>mcp:{m}</span>)}
        </div>) : null}
      {!rejecting ? (
        <div className="actions">
          <button onClick={onApprove}>Approve &amp; run this step</button>
          <button className="ghost" onClick={() => setRejecting(true)}>Reject…</button>
        </div>
      ) : (
        <>
          <label>Why are you rejecting? The reason is stored on the run.</label>
          <input autoFocus value={reason} placeholder="e.g. the diff touches migrations" onChange={e => setReason(e.target.value)} />
          <div className="actions">
            <button className="danger" disabled={!reason.trim()} onClick={() => onReject(reason.trim())}>Confirm reject</button>
            <button className="ghost" onClick={() => { setRejecting(false); setReason('') }}>Cancel</button>
          </div>
        </>)}
    </div>
  )
}
