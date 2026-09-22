import { useEffect, useState } from 'react'
import { api, type BuiltinTool, type Skill, type SkillRegistry } from '../api'
import DataTable, { type Column, type Filter } from '../components/DataTable'

/** Skills and built-in tools are the two catalogues a step draws from, and a
 *  step may only name what is in them (ADR 0004: scoping is the security
 *  model). So this page is a reference you search, not a page you read. */
export default function SkillsPage() {
  const [reg, setReg] = useState<SkillRegistry>()
  const [tools, setTools] = useState<BuiltinTool[]>()
  const [err, setErr] = useState('')
  const [tab, setTab] = useState<'skills' | 'tools' | 'shadowed'>('skills')

  useEffect(() => {
    api.skills().then(setReg).catch(e => setErr(String(e.message || e)))
    api.tools().then(setTools).catch(e => setErr(String(e.message || e)))
  }, [])

  const skills = reg?.skills || []
  const skipped = reg?.skipped || []

  const skillCols: Column<Skill>[] = [
    { key: 'name', header: 'Skill', width: 210, value: s => s.name, cell: s => <span className="mono" style={{ fontWeight: 500 }}>{s.name}</span> },
    { key: 'source', header: 'Root', width: 110, value: s => s.source, cell: s => <span className="pill">{s.source}</span> },
    { key: 'description', header: 'What it is for', value: s => s.description || '', cell: s => s.description || <i className="muted">no description in frontmatter</i> },
    {
      key: 'shadows', header: 'Shadows', width: 90, align: 'right',
      value: s => s.shadowed?.length || 0,
      // A shadowed skill is a name that silently resolves to a different file
      // than somebody expects, so the count is worth a column of its own.
      cell: s => s.shadowed?.length
        ? <span className="badge awaiting_approval" title={s.shadowed.join('\n')}>{s.shadowed.length}</span>
        : <span className="muted">—</span>,
    },
    { key: 'location', header: 'File', value: s => s.location, cell: s => <span className="mono muted" title={s.location}>{tail(s.location, 44)}</span> },
  ]

  const skillFilters: Filter<Skill>[] = [
    { key: 'source', label: 'roots', options: [...new Set(skills.map(s => s.source))].sort(), match: (s, v) => s.source === v },
    { key: 'shadowed', label: 'shadowing', options: ['shadows others'], match: s => !!s.shadowed?.length },
  ]

  const toolCols: Column<BuiltinTool>[] = [
    { key: 'name', header: 'Tool', width: 170, value: t => t.name, cell: t => <span className="mono" style={{ fontWeight: 500 }}>{t.name}</span> },
    { key: 'description', header: 'What it does', value: t => t.description, cell: t => t.description },
  ]

  return (
    <>
      <div className="head">
        <div>
          <h1>Skills &amp; tools</h1>
          <p>
            Everything a step may name. A step sees exactly what its YAML lists and nothing else,
            so a name missing here is a name a workflow cannot use.
          </p>
        </div>
      </div>
      {err && <div className="banner err">{err}</div>}

      <div className="card">
        <div className="subhead" style={{ gap: 4 }}>
          <button className={tab === 'skills' ? '' : 'ghost'} onClick={() => setTab('skills')}>Skills {skills.length}</button>
          <button className={tab === 'tools' ? '' : 'ghost'} onClick={() => setTab('tools')}>Built-in tools {tools?.length ?? 0}</button>
          <button className={tab === 'shadowed' ? '' : 'ghost'} onClick={() => setTab('shadowed')}>Refused {skipped.length}</button>
          {reg && <span className="muted" style={{ marginLeft: 'auto', fontSize: 12 }}>
            roots in precedence order: {reg.roots.join(' → ')}
          </span>}
        </div>

        {tab === 'skills' && (
          <DataTable rows={skills} columns={skillCols} filters={skillFilters} getKey={s => s.location}
            initialSort={{ key: 'name', dir: 'asc' }} searchPlaceholder="Search skills…"
            empty="No skills found in any root." />)}

        {tab === 'tools' && (
          <DataTable rows={tools || []} columns={toolCols} getKey={t => t.name}
            initialSort={{ key: 'name', dir: 'asc' }} searchPlaceholder="Search tools…"
            empty="No built-in tools reported." />)}

        {tab === 'shadowed' && (
          <DataTable
            rows={skipped}
            columns={[
              { key: 'reason', header: 'Why', width: 180, value: s => s.reason, cell: s => <span className="pill">{s.reason}</span> },
              { key: 'location', header: 'File', value: s => s.location, cell: s => <span className="mono">{s.location}</span> },
            ]}
            getKey={s => s.location} initialSort={{ key: 'reason', dir: 'asc' }}
            searchPlaceholder="Search refused…"
            empty="Every skill file in every root loaded." />)}
      </div>
    </>
  )
}

/** tail keeps the END of a path, which is the part that identifies it. */
function tail(s: string, n: number) { return s.length > n ? '…' + s.slice(-n) : s }
