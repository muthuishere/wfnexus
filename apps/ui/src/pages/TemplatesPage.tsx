import { useCallback, useEffect, useState } from 'react'
import { api, type Template } from '../api'

/** The gallery: workflows that exist to be copied.
 *
 *  A template gives you the SHAPE — the phases, the typed hand-off between
 *  them, the gates that stop for a human — and leaves the expertise to you.
 *  That is why most ship with no skills on any step: the five phases of a bug
 *  fix are the same everywhere, and what a good report looks like in your shop
 *  is not. */
export default function TemplatesPage() {
  const [templates, setTemplates] = useState<Template[]>()
  const [err, setErr] = useState('')

  const load = useCallback(() => {
    api.templates().then(t => { setTemplates(t); setErr('') })
      .catch(e => setErr(e instanceof Error ? e.message : String(e)))
  }, [])
  useEffect(load, [load])

  return (
    <>
      <div className="head">
        <div>
          <h1>Templates</h1>
          <p>
            A starting point, not a black box. Copying one gives you an ordinary workflow file you
            own and can edit — the shape and the contracts are already wired, and the skills on each
            phase are yours to add.
          </p>
        </div>
      </div>

      {err && <div className="banner err">{err}</div>}
      {!templates ? <div className="card muted">loading…</div>
        : templates.length === 0
          ? <div className="card muted">
            No templates. A workflow becomes one by carrying <span className="mono">template:</span> and
            living in the templates directory.
          </div>
          : templates.map(t => <TemplateCard key={t.name} t={t} onCopied={load} onError={setErr} />)}
    </>
  )
}

function TemplateCard({ t, onCopied, onError }: { t: Template; onCopied: () => void; onError: (s: string) => void }) {
  const [as, setAs] = useState(t.name)
  const [busy, setBusy] = useState(false)

  const copy = () => {
    setBusy(true)
    api.copyTemplate(t.name, as.trim())
      .then(r => { location.hash = `#/workflows/${encodeURIComponent(r.name)}/edit`; onCopied() })
      .catch(e => onError(String(e.message || e)))
      .finally(() => setBusy(false))
  }

  return (
    <div className="card" style={{ marginBottom: 16 }}>
      <div className="subhead">
        <h2>{t.title}</h2>
        {t.needsSkills && <span className="pill">skills to add</span>}
        <span className="muted mono" style={{ fontSize: 12 }}>{t.name}</span>
      </div>
      <p className="muted" style={{ marginBottom: 14 }}>{t.summary}</p>

      {/* The phases, in order — the shape someone is actually choosing between. */}
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, alignItems: 'stretch', marginBottom: 14 }}>
        {t.phases.map((p, i) => (
          <div key={p.id} style={{ display: 'contents' }}>
            {i > 0 && <div className="muted" style={{ alignSelf: 'center' }}>→</div>}
            <div style={{
              border: '1px solid var(--line, #ddd)', borderRadius: 8, padding: '8px 12px',
              minWidth: 150, maxWidth: 220,
            }}>
              <div className="mono" style={{ fontWeight: 550, fontSize: 13 }}>{p.id}</div>
              <div className="muted" style={{ fontSize: 12, marginTop: 2 }}>{p.name || p.kind}</div>
              <div style={{ marginTop: 6, display: 'flex', gap: 4, flexWrap: 'wrap' }}>
                {p.approval && <span className="badge awaiting_approval">human gate</span>}
                {(!p.skills || !p.skills.length) && p.kind === 'prompt' && <span className="pill">no skills yet</span>}
                {p.skills?.map(s => <span key={s} className="pill">{s}</span>)}
              </div>
            </div>
          </div>))}
      </div>

      {!!t.fill?.length && (
        <>
          <div className="muted" style={{ fontSize: 12, marginBottom: 4 }}>What is yours to supply:</div>
          <ul className="muted" style={{ fontSize: 13, marginBottom: 14, paddingLeft: 18 }}>
            {t.fill.map((f, i) => <li key={i}>{f}</li>)}
          </ul>
        </>)}

      <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
        <input value={as} onChange={e => setAs(e.target.value)} placeholder="name your copy"
          className="mono" style={{ maxWidth: 260 }} />
        <button disabled={busy || !as.trim()} onClick={copy}>
          {busy ? 'Copying…' : 'Copy and edit'}
        </button>
        <span className="muted" style={{ fontSize: 12 }}>
          writes a workflow you own, then opens it in the builder
        </span>
      </div>
    </div>
  )
}
