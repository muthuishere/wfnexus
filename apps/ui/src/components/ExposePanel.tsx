import { useCallback, useEffect, useState } from 'react'
import { api, type ExposeIssued, type ExposeKind, type ExposeView } from '../api'

/** Expose — let something outside start this workflow (ADR 0027).
 *
 *  Two doors, both off until a token is issued here: an inbound HOOK URL, and
 *  an MCP endpoint that lists the workflow as a tool. The token is shown ONCE,
 *  right after it is issued or rotated; only its prefix is kept. */
export default function ExposePanel({ workflow }: { workflow: string }) {
  const [view, setView] = useState<ExposeView>()
  const [issued, setIssued] = useState<ExposeIssued & { kind: ExposeKind }>()
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  const load = useCallback(() => {
    api.workflowExpose(workflow)
      .then(v => { setView(v); setErr('') })
      .catch(e => setErr(e instanceof Error ? e.message : String(e)))
  }, [workflow])
  useEffect(load, [load])

  const issue = (kind: ExposeKind, rotate: boolean) => {
    if (rotate && !confirm(`Rotate the ${kind} token? The current one stops working immediately.`)) return
    setBusy(true)
    api.issueExpose(workflow, kind, rotate)
      .then(r => { setIssued({ ...r, kind }); setView(r) })
      .catch(e => setErr(String(e.message || e)))
      .finally(() => setBusy(false))
  }
  const revoke = (kind: ExposeKind) => {
    if (!confirm(`Turn off the ${kind}? Anything using its token stops working.`)) return
    setBusy(true)
    api.revokeExpose(workflow, kind)
      .then(() => { setIssued(undefined); load() })
      .catch(e => setErr(String(e.message || e)))
      .finally(() => setBusy(false))
  }

  const door = (kind: ExposeKind, title: string, blurb: string) => {
    const v = view?.[kind]
    return (
      <div style={{ marginBottom: 16 }}>
        <div className="subhead">
          <h3>{title}</h3>
          <span className={v?.enabled ? 'pill ok' : 'pill'}>{v?.enabled ? 'on' : 'off'}</span>
        </div>
        <p className="muted">{blurb}</p>
        {v && <p><code>{v.url}</code></p>}
        {v?.enabled && (
          <p className="muted">
            token <code>{v.prefix}…</code>
            {v.lastUsedAt ? ` · last used ${new Date(v.lastUsedAt).toLocaleString()}` : ' · never used'}
          </p>
        )}
        <div style={{ display: 'flex', gap: 8 }}>
          {!v?.enabled && <button disabled={busy} onClick={() => issue(kind, false)}>Expose</button>}
          {v?.enabled && <button className="ghost" disabled={busy} onClick={() => issue(kind, true)}>Rotate token</button>}
          {v?.enabled && <button className="ghost danger-text" disabled={busy} onClick={() => revoke(kind)}>Revoke</button>}
        </div>
        {issued?.kind === kind && (
          <div className="card" style={{ marginTop: 12 }}>
            <strong>Copy this token now — it will not be shown again.</strong>
            <pre style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>{issued.token}</pre>
            {issued.signingSecret && (<>
              <div className="muted">Signing secret, for an X-WFX-Signature HMAC instead of the bearer token (also shown once)</div>
              <pre style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>{issued.signingSecret}</pre>
            </>)}
            {issued.signingNote && <div className="muted">{issued.signingNote}</div>}
            <div className="muted">{kind === 'mcp' ? 'MCP client config' : 'Try it'}</div>
            <pre style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
              {typeof issued.snippet === 'string' ? issued.snippet : JSON.stringify(issued.snippet, null, 2)}
            </pre>
          </div>
        )}
      </div>
    )
  }

  return (
    <div className="card" style={{ marginBottom: 16 }}>
      <div className="subhead"><h2>Expose</h2></div>
      {err && <p className="error">{err}</p>}
      {door('hook', 'Hook URL',
        'POST JSON here with Authorization: Bearer <token> (or an X-WFX-Signature HMAC) and the body becomes the run\'s input. Add ?wait=1 to get the output back; send Idempotency-Key to make retries safe.')}
      {door('mcp', 'MCP endpoint',
        `Any MCP client (streamable HTTP) sees this workflow as the tool "${view?.tool ?? ''}"; calling it starts a run and returns its output, or the run id if it takes longer.`)}
      {view && (
        <p className="muted">
          One config for every opted-in workflow of this project: <code>{view.projectMcp.url}</code> with a
          project token from <code>wfx expose mcp --project {view.project}</code>.
        </p>
      )}
    </div>
  )
}
