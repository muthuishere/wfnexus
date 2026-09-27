import { useState } from 'react'
import { api, type Template } from '../api'
import { href } from '../lib/routes'
import { isProposal, lifecycle, type Proposal } from '../lifecycle'
import { ProposedNotice } from './Changes'

const msg = (e: unknown) => (e instanceof Error ? e.message : String(e))

/** Onboarding: a git URL and a branch. A repo that already has
 *  .wfx/workflows/ opens straight away; one that has none is offered starter
 *  templates, because an empty project is a dead end without a first step. */
export default function ConnectRepo({ onDone }: { onDone?: () => void }) {
  const [url, setUrl] = useState('')
  const [branch, setBranch] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [empty, setEmpty] = useState<string>()
  const [starters, setStarters] = useState<Template[]>([])
  const [proposed, setProposed] = useState<Proposal>()

  const connect = () => {
    setBusy(true); setErr('')
    lifecycle.connect({ url: url.trim(), branch: branch.trim() || undefined })
      .then(async p => {
        onDone?.()
        if (p.workflows?.length) { location.hash = href.project(p.name); return }
        setEmpty(p.name)
        setStarters((await api.templates().catch(() => [])).slice(0, 6))
      })
      .catch(e => setErr(msg(e)))
      .finally(() => setBusy(false))
  }

  const use = (t: Template) => {
    if (!empty) return
    setBusy(true); setErr('')
    api.copyTemplate(t.name, t.name, empty)
      .then(r => { if (isProposal(r)) setProposed(r); else location.hash = href.workflow(empty, t.name) })
      .catch(e => setErr(msg(e)))
      .finally(() => setBusy(false))
  }

  return (
    <div className="card connect" aria-label="Connect a repo">
      <div className="subhead"><h2>Connect a repo</h2></div>
      {!empty ? (
        <>
          <div className="grid2">
            <div className="bfield">
              <label htmlFor="connect-url">Git URL</label>
              <input id="connect-url" className="mono" value={url} autoFocus onChange={e => setUrl(e.target.value)}
                placeholder="https://github.com/you/repo.git" />
            </div>
            <div className="bfield">
              <label htmlFor="connect-branch">Branch</label>
              <input id="connect-branch" className="mono" value={branch} onChange={e => setBranch(e.target.value)} placeholder="main" />
            </div>
          </div>
          <div className="actions">
            <button disabled={!url.trim() || busy} onClick={connect}>{busy ? 'Connecting…' : 'Connect'}</button>
          </div>
        </>
      ) : (
        <>
          <p>
            <b className="mono">{empty}</b> is connected, but has no <span className="mono">.wfx/workflows/</span> yet.
            Start from a template:
          </p>
          {proposed && <ProposedNotice proposal={proposed} />}
          <div className="starters">
            {starters.map(t => (
              <div key={t.name} className="starter">
                <b>{t.title || t.name}</b>
                <p className="muted">{t.summary}</p>
                <button className="ghost small" disabled={busy} onClick={() => use(t)}>Use {t.name}</button>
              </div>))}
          </div>
          <div className="actions">
            <a href={href.fromTemplate(empty)}>All templates</a>
            <a href={href.project(empty)}>Open the project</a>
          </div>
        </>
      )}
      {err && <div className="banner err">{err}</div>}
    </div>
  )
}
