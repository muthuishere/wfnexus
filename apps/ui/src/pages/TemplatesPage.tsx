import { useCallback, useEffect, useMemo, useState } from 'react'
import { api, type Template } from '../api'
import FileList from '../components/FileList'
import { href } from '../lib/routes'
import Crumbs from '../components/Crumbs'
import { ProposedNotice } from '../components/Changes'
import { isProposal, type Proposal } from '../lifecycle'
import { labelOf, useCategories } from '../lib/categories'

/** The gallery: workflows that exist to be copied.
 *
 *  A template gives you the SHAPE — the phases, the typed hand-off between
 *  them, the gates that stop for a human — and leaves the expertise to you.
 *  That is why most ship with no skills on any step: the five phases of a bug
 *  fix are the same everywhere, and what a good report looks like in your shop
 *  is not. */
export default function TemplatesPage({ project }: { project?: string }) {
  const [templates, setTemplates] = useState<Template[]>()
  const [err, setErr] = useState('')
  const [category, setCategory] = useState<string>()
  const [filter, setFilter] = useState('')
  const cats = useCategories()

  const load = useCallback(() => {
    api.templates().then(t => { setTemplates(t); setErr('') })
      .catch(e => setErr(e instanceof Error ? e.message : String(e)))
  }, [])
  useEffect(load, [load])
  // Opened from a project: that project's category leads, so a finance team
  // sees finance templates before anyone else's.
  useEffect(() => {
    if (!project) return
    api.project(project).then(p => setCategory(p.category)).catch(() => {})
  }, [project])

  // Sections in the category list's order, the project's own first, then
  // uncategorised; a filter chip narrows to one.
  const sections = useMemo(() => {
    const order = [...cats.map(c => c.id), '']
    if (category) order.splice(order.indexOf(category), 1), order.unshift(category)
    const by = new Map<string, Template[]>()
    for (const t of templates || []) {
      const k = t.category && order.includes(t.category) ? t.category : ''
      by.set(k, [...(by.get(k) || []), t])
    }
    return order.filter(id => by.has(id) && (!filter || id === filter))
      .map(id => ({ id, label: labelOf(cats, id), items: by.get(id)! }))
  }, [templates, cats, category, filter])
  const present = useMemo(() => new Set((templates || []).map(t => t.category || '')), [templates])

  return (
    <>
      {project && <Crumbs items={[['Projects', href.projects()], [project, href.project(project)]]} />}
      <div className="head">
        <div>
          <h1>{project ? `Start from a template in ${project}` : 'Templates'}</h1>
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
          : <>
            {present.size > 1 && (
              <div className="chips">
                <button className={!filter ? 'active' : ''} onClick={() => setFilter('')}>All</button>
                {cats.filter(c => present.has(c.id)).map(c => (
                  <button key={c.id} className={filter === c.id ? 'active' : ''} onClick={() => setFilter(c.id)}>
                    {c.label}{c.id === category && ' · this project'}
                  </button>))}
              </div>)}
            {sections.map(sec => (
              <section key={sec.id || '_none'}>
                {(sections.length > 1 || sec.id) && (
                  <div className="grouphead">
                    <span>{sec.label}{sec.id && sec.id === category && <span className="pill" style={{ marginLeft: 8 }}>this project's kind of work</span>}</span>
                    <span className="muted">{sec.items.length}</span>
                  </div>)}
                {sec.items.map(t => <TemplateCard key={t.name} t={t} project={project} onCopied={load} onError={setErr} />)}
              </section>))}
          </>}
    </>
  )
}

function TemplateCard({ t, project, onCopied, onError }: { t: Template; project?: string; onCopied: () => void; onError: (s: string) => void }) {
  const [as, setAs] = useState(t.name)
  const [busy, setBusy] = useState(false)
  const [proposal, setProposal] = useState<Proposal>()

  const copy = () => {
    setBusy(true)
    // Into the project the gallery was opened from; from the top-level
    // gallery, the platform's own.
    api.copyTemplate(t.name, as.trim(), project)
      .then(r => {
        // Into a repo, a copy is a proposed change, not a written file.
        if (isProposal(r)) { setProposal(r); return }
        location.hash = href.edit(project || 'local', r.name); onCopied()
      })
      .catch(e => onError(String(e.message || e)))
      .finally(() => setBusy(false))
  }

  return (
    <div className="card" style={{ marginBottom: 16 }}>
      {proposal && <ProposedNotice proposal={proposal} />}
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

      {/* What ELSE comes with it. A template whose step says `run: report.js`
          is unreadable until you can see report.js is there — and copying
          brings all of this, so all of it is shown. */}
      {!!t.files?.length && (
        <>
          <div className="muted" style={{ fontSize: 12, marginBottom: 4 }}>
            Files that come with it ({t.files.length}):
          </div>
          <div style={{ maxWidth: 420, marginBottom: 14 }}><FileList files={t.files} /></div>
        </>)}

      <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
        <input value={as} onChange={e => setAs(e.target.value)} placeholder="name your copy"
          className="mono" style={{ maxWidth: 260 }} />
        <button disabled={busy || !as.trim()} onClick={copy}>
          {busy ? 'Copying…' : 'Copy and edit'}
        </button>
        <span className="muted" style={{ fontSize: 12 }}>
          {project ? <>writes it into <b>{project}</b>, then opens it in the builder</> : 'writes a workflow you own into local, then opens it in the builder'}
        </span>
      </div>
    </div>
  )
}
