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

/** A picker over a known catalogue — skills, built-in tools, MCP servers.
 *
 *  These are allowlists: a name not in the catalogue makes the loader reject
 *  the whole workflow, so free text would be a trap and the list has to be
 *  browsable. The whole design problem is that it has to stay browsable at 85
 *  skills whose descriptions are each a paragraph long.
 *
 *  So: one line per entry, the description clipped to its first sentence, the
 *  full text on hover and on the expanded row. Nothing is hidden — it is
 *  summarised, and the summary is the part that tells you whether you want it.
 */
export function Picker({ title, options, selected, onChange, groupOf, emptyNote }: {
  title: string
  options: Array<{ name: string; description?: string; group?: string }>
  selected: string[]
  onChange: (next: string[]) => void
  groupOf?: (name: string) => string
  emptyNote?: string
}) {
  const [q, setQ] = useState('')
  const [openRow, setOpenRow] = useState<string>()
  const [showAll, setShowAll] = useState(false)
  const needle = q.trim().toLowerCase()

  const matched = useMemo(() => options
    .map(o => ({ ...o, group: o.group || groupOf?.(o.name) || '' }))
    .filter(o => !needle || `${o.name} ${o.description || ''}`.toLowerCase().includes(needle)),
    [options, needle, groupOf])

  // Selected entries first, then the rest: what you have chosen is what you
  // are most likely to want to change, and it must not be lost below a fold.
  const ordered = useMemo(() => {
    const picked = matched.filter(o => selected.includes(o.name))
    const rest = matched.filter(o => !selected.includes(o.name))
    return [...picked, ...rest]
  }, [matched, selected])

  // A catalogue this long is capped until asked for. Searching is the intended
  // way through it, so the cap is lifted by searching as well as by the button.
  const CAP = 12
  const capped = showAll || needle ? ordered : ordered.slice(0, CAP)
  const hidden = ordered.length - capped.length

  const toggle = (name: string) =>
    onChange(selected.includes(name) ? selected.filter(s => s !== name) : [...selected, name])

  return (
    <div className="picker">
      <div className="pickhead">
        <b>{title}</b>
        <span className="muted">{selected.length} selected · {options.length} available</span>
        <input value={q} placeholder={`Search ${title.toLowerCase()}…`}
          onChange={e => setQ(e.target.value)} />
      </div>

      {selected.length > 0 && (
        <div className="chips picked">
          {selected.map(s => (
            <span key={s} className={options.some(o => o.name === s) ? '' : 'unknown'}
              title={options.some(o => o.name === s) ? undefined : 'not in the catalogue — the loader will refuse this workflow'}>
              {s}<button type="button" onClick={() => toggle(s)} aria-label={`remove ${s}`}>×</button>
            </span>))}
        </div>)}

      {options.length === 0 && <div className="muted">{emptyNote || 'catalogue unavailable'}</div>}

      <div className="picklist">
        {capped.map(o => {
          const on = selected.includes(o.name)
          const open = openRow === o.name
          const full = o.description || ''
          const brief = firstSentence(full)
          return (
            <div key={o.name} className={`popt${on ? ' on' : ''}`}>
              <label>
                <input type="checkbox" checked={on} onChange={() => toggle(o.name)} />
                <span className="pname mono">{o.name}</span>
                {o.group && <span className="pgrouptag">{o.group}</span>}
                <span className="pdesc" title={full}>{open ? full : brief}</span>
              </label>
              {full.length > brief.length && (
                <button type="button" className="pmore"
                  onClick={() => setOpenRow(open ? undefined : o.name)}
                  aria-label={open ? 'less' : 'more'}>{open ? '−' : '…'}</button>)}
            </div>)
        })}
        {hidden > 0 && (
          <button type="button" className="pshowall" onClick={() => setShowAll(true)}>
            Show {hidden} more — or search
          </button>)}
        {options.length > 0 && ordered.length === 0 && (
          <div className="muted" style={{ padding: '10px 2px' }}>Nothing matches “{q}”.</div>)}
      </div>
    </div>
  )
}

/** firstSentence keeps enough to decide with and no more. These descriptions
 *  are written as "what it is — when to use it, trigger on: …", and the part
 *  before the first full stop is reliably the part that identifies it. */
function firstSentence(s: string, max = 120): string {
  if (!s) return ''
  const stop = s.search(/[.—]\s/)
  let out = stop > 20 ? s.slice(0, stop + 1) : s
  if (out.length > max) out = out.slice(0, max).replace(/\s+\S*$/, '') + '…'
  return out
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
