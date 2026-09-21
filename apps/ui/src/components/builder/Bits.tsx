import { useMemo, useState, type ReactNode } from 'react'
import type { Skill } from '../../api'
import type { Issue } from '../../builder/validate'

export function Field({ label, hint, children, issues }: { label: string; hint?: ReactNode; children: ReactNode; issues?: Issue[] }) {
  return (
    <div className="bfield">
      <label>{label}</label>
      {children}
      {hint && <div className="hint">{hint}</div>}
      <IssueList issues={issues} />
    </div>
  )
}

export function IssueList({ issues }: { issues?: Issue[] }) {
  if (!issues?.length) return null
  return (
    <div className="issues">
      {issues.map((i, n) => (
        <div key={n} className={`issue ${i.severity}`}>
          <span className="mark">{i.severity === 'error' ? '✕' : '!'}</span>{i.message}
        </div>))}
    </div>
  )
}

export function Section({ title, count, hint, children, defaultOpen = false, tone }:
{ title: string; count?: number; hint?: ReactNode; children: ReactNode; defaultOpen?: boolean; tone?: 'err' }) {
  const [open, setOpen] = useState(defaultOpen)
  return (
    <div className={`bsection${tone === 'err' ? ' has-err' : ''}`}>
      <button type="button" className="bsechead" onClick={() => setOpen(!open)}>
        <span className="caret">{open ? '▾' : '▸'}</span>
        <span className="t">{title}</span>
        {count !== undefined && count > 0 && <span className="badge">{count}</span>}
        {tone === 'err' && <span className="badge failed">needs attention</span>}
      </button>
      {open && <div className="bsecbody">{hint && <div className="hint" style={{ marginBottom: 8 }}>{hint}</div>}{children}</div>}
    </div>
  )
}

/** A checkbox picker over a known catalog, with search. Used for skills and
 *  built-in tools — both are allowlists, and a name not in the catalog makes
 *  the backend reject the entire file, so free text would be a trap. */
export function Picker({ title, options, selected, onChange, groupOf, emptyNote }: {
  title: string
  options: Array<{ name: string; description?: string; group?: string }>
  selected: string[]
  onChange: (next: string[]) => void
  groupOf?: (name: string) => string
  emptyNote?: string
}) {
  const [q, setQ] = useState('')
  const needle = q.trim().toLowerCase()
  const groups = useMemo(() => {
    const g: Array<[string, typeof options]> = []
    for (const o of options) {
      if (needle && !`${o.name} ${o.description || ''}`.toLowerCase().includes(needle)) continue
      const key = o.group || groupOf?.(o.name) || ''
      const hit = g.find(([k]) => k === key)
      if (hit) hit[1].push(o); else g.push([key, [o]])
    }
    return g
  }, [options, needle, groupOf])
  const toggle = (name: string) =>
    onChange(selected.includes(name) ? selected.filter(s => s !== name) : [...selected, name])

  return (
    <div className="picker">
      <div className="pickhead">
        <b>{title}</b>
        <span className="muted">{selected.length} selected</span>
        <input value={q} placeholder="filter…" onChange={e => setQ(e.target.value)} />
      </div>
      {selected.length > 0 && (
        <div className="chips picked">
          {selected.map(s => (
            <span key={s} className={options.some(o => o.name === s) ? '' : 'unknown'}>
              {s}<button type="button" onClick={() => toggle(s)} aria-label={`remove ${s}`}>×</button>
            </span>))}
        </div>)}
      {options.length === 0 && <div className="muted">{emptyNote || 'catalog unavailable'}</div>}
      <div className="picklist">
        {groups.map(([g, list]) => (
          <div key={g || '_'}>
            {g && <div className="pgroup">{g} <span className="muted">· {list.length}</span></div>}
            {list.map(o => (
              <label key={o.name} className="popt">
                <input type="checkbox" checked={selected.includes(o.name)} onChange={() => toggle(o.name)} />
                <span>
                  <b className="mono">{o.name}</b>
                  {o.description && <i className="muted"> — {o.description}</i>}
                </span>
              </label>))}
          </div>))}
        {options.length > 0 && groups.length === 0 && <div className="muted">nothing matches “{q}”</div>}
      </div>
    </div>
  )
}

/** A free-text list: mcp servers, args_contain fragments, score rubric levels. */
export function StringList({ values, onChange, placeholder, addLabel, multiline }: {
  values: string[]; onChange: (v: string[]) => void; placeholder?: string; addLabel?: string; multiline?: boolean
}) {
  const set = (i: number, v: string) => onChange(values.map((x, j) => (j === i ? v : x)))
  return (
    <div className="strlist">
      {values.map((v, i) => (
        <div className="row" key={i}>
          <span className="idx mono">{i}</span>
          {multiline
            ? <textarea style={{ minHeight: 46 }} value={v} placeholder={placeholder} onChange={e => set(i, e.target.value)} />
            : <input value={v} placeholder={placeholder} onChange={e => set(i, e.target.value)} />}
          <button type="button" className="ghost small" onClick={() => onChange(values.filter((_, j) => j !== i))}>×</button>
        </div>))}
      <button type="button" className="ghost small" onClick={() => onChange([...values, ''])}>+ {addLabel || 'add'}</button>
    </div>
  )
}

/** One entry per skill NAME — a shadowed duplicate is the same allowlist entry,
 *  and offering it twice would let an author "pick" a skill that never loads. */
export const skillOptions = (skills: Skill[]) => {
  const seen = new Set<string>()
  return skills.filter(s => !seen.has(s.name) && seen.add(s.name))
    .map(s => ({ name: s.name, description: s.description, group: s.source }))
}
