import { useEffect, useState } from 'react'
import { api, type BuiltinTool, type Skill, type SkillRegistry } from '../api'

const SOURCE_LABEL: Record<string, string> = {
  project: 'project · ./skills',
  claude: 'claude · ~/.claude/skills',
  agents: 'agents · ~/.agents/skills',
}

function SkillRow({ s }: { s: Skill }) {
  return (
    <div className="skill">
      <div className="titleline">
        <b className="mono">{s.name}</b>
        <span className={`badge src-${s.source}`}>{s.source}</span>
        {s.shadowed && s.shadowed.length > 0 && <span className="badge awaiting_approval">shadows {s.shadowed.length}</span>}
      </div>
      <div className="muted">{s.description || <i>no description in frontmatter</i>}</div>
      <div className="mono muted loc">{s.location}</div>
      {s.shadowed && s.shadowed.length > 0 && (
        <div className="shadow">
          This root wins, so these same-named skills are never loaded:
          {s.shadowed.map(l => <div className="mono" key={l}>{l}</div>)}
        </div>)}
    </div>
  )
}

export default function SkillsPage() {
  const [reg, setReg] = useState<SkillRegistry>()
  const [tools, setTools] = useState<BuiltinTool[]>()
  const [skillsErr, setSkillsErr] = useState('')
  const [toolsErr, setToolsErr] = useState('')
  const [q, setQ] = useState('')

  useEffect(() => {
    api.skills().then(setReg).catch(e => setSkillsErr(e.message))
    api.tools().then(setTools).catch(e => setToolsErr(e.message))
  }, [])

  const needle = q.trim().toLowerCase()
  const match = (...fields: string[]) => !needle || fields.some(f => (f || '').toLowerCase().includes(needle))

  const grouped: Array<[string, Skill[]]> = []
  for (const s of reg?.skills || []) {
    if (!match(s.name, s.description, s.location, s.source)) continue
    const g = grouped.find(([k]) => k === s.source)
    if (g) g[1].push(s); else grouped.push([s.source, [s]])
  }

  const shownTools = (tools || []).filter(t => match(t.name, t.description))
  const total = reg?.skills.length ?? 0
  const shown = grouped.reduce((n, [, v]) => n + v.length, 0)

  if (!reg && !tools && !skillsErr && !toolsErr) return <div className="muted">loading…</div>

  return (
    <>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 14 }}>
        <div>
          <h1>Registries</h1>
          <div className="muted">Everything a step's <span className="mono">skills:</span> and <span className="mono">tools:</span> allowlists may draw from.</div>
        </div>
        <input
          style={{ marginLeft: 'auto', maxWidth: 320 }} value={q}
          placeholder="filter skills and tools…" onChange={e => setQ(e.target.value)} />
      </div>

      {skillsErr && <div className="banner err">Skill registry unavailable — {skillsErr}</div>}

      {reg && (
        <div className="card">
          <h3>Roots · precedence order</h3>
          {reg.roots.length === 0 ? <div className="muted">No skill roots configured.</div> : (
            <ol className="roots">
              {reg.roots.map((r, i) => (
                <li key={r}><span className="mono">{r}</span> {i === 0 && <span className="muted">— wins on a name clash</span>}</li>))}
            </ol>)}
          <div className="muted" style={{ fontSize: 12 }}>
            A skill found in an earlier root <b>shadows</b> the same name in a later one; only the winner is ever loaded into an agent.
          </div>
        </div>)}

      {reg && (
        <div className={`card ${reg.skipped.length ? 'skipped-card' : ''}`}>
          <h3>Skipped</h3>
          {reg.skipped.length === 0 ? (
            <div className="banner ok">Every skill on disk parsed cleanly — nothing is silently missing.</div>
          ) : (
            <>
              <div className="banner err">
                <b>{reg.skipped.length} skill{reg.skipped.length > 1 ? 's were' : ' was'} not loaded.</b> A step listing one of these
                gets no error — the skill is simply absent from its agent. Fix the frontmatter.
              </div>
              {reg.skipped.map(s => (
                <div className="skipped" key={s.location}>
                  <div className="mono">{s.location}</div>
                  <div style={{ color: 'var(--err)' }}>{s.reason}</div>
                </div>))}
            </>)}
        </div>)}

      {reg && (
        <div className="card">
          <div style={{ display: 'flex', alignItems: 'center' }}>
            <h3 style={{ margin: 0 }}>Skills</h3>
            <span className="muted" style={{ marginLeft: 'auto', fontSize: 12 }}>{needle ? `${shown} of ${total}` : `${total} loaded`}</span>
          </div>
          {total === 0 && <div className="muted" style={{ marginTop: 8 }}>No skills in any root.</div>}
          {total > 0 && shown === 0 && <div className="muted" style={{ marginTop: 8 }}>Nothing matches “{q}”.</div>}
          {grouped.map(([source, list]) => (
            <div className="group" key={source}>
              <h3>{SOURCE_LABEL[source] || source} <span className="muted">· {list.length}</span></h3>
              <div className="skills">{list.map(s => <SkillRow key={s.location} s={s} />)}</div>
            </div>))}
        </div>)}

      <div className="card">
        <div style={{ display: 'flex', alignItems: 'center' }}>
          <h3 style={{ margin: 0 }}>Built-in tools</h3>
          <span className="muted" style={{ marginLeft: 'auto', fontSize: 12 }}>{tools?.length ?? 0} shipped by toolnexus</span>
        </div>
        <div className="muted" style={{ fontSize: 12, margin: '6px 0 10px' }}>
          A step's <span className="mono">tools:</span> list is an allowlist over exactly these. Omit a name and the step cannot call it at all.
        </div>
        {toolsErr && <div className="banner err">Tool catalog unavailable — {toolsErr}</div>}
        {!tools && !toolsErr && <div className="muted">loading…</div>}
        {tools && tools.length === 0 && <div className="muted">The API reported no built-ins.</div>}
        {tools && tools.length > 0 && shownTools.length === 0 && <div className="muted">Nothing matches “{q}”.</div>}
        <div className="tools">
          {shownTools.map(t => (
            <div className="tool" key={t.name}>
              <b className="mono">{t.name}</b>
              <div className="muted">{t.description}</div>
            </div>))}
        </div>
      </div>
    </>
  )
}
