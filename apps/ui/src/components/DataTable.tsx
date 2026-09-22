import { useMemo, useState, type ReactNode } from 'react'

/** One table, used by every list in the app.
 *
 *  Search, sort, filter and pagination are the four things every list here
 *  needs and none of them is interesting enough to write five times — writing
 *  them once also means they behave identically, so learning the runs table
 *  teaches you the providers table.
 *
 *  Everything is client-side. The API returns at most a few hundred rows today
 *  (100 runs, a handful of workflows, ~85 skills) and a server-side pager for
 *  that would be machinery without a problem. When a list genuinely outgrows
 *  it, the seam to change is here and only here. */
export type Column<T> = {
  key: string
  header: string
  /** what the cell renders */
  cell: (row: T) => ReactNode
  /** the value sorted and searched on; defaults to the cell when it is a string */
  value?: (row: T) => string | number
  sortable?: boolean
  width?: number | string
  align?: 'right'
}

export type Filter<T> = {
  key: string
  label: string
  /** the options offered; "" is always added as "all" */
  options: string[]
  match: (row: T, value: string) => boolean
}

export default function DataTable<T>({
  rows, columns, filters = [], getKey, onRowClick, rowClass,
  searchPlaceholder = 'Search…', empty = 'Nothing here yet.', pageSize = 25,
  initialSort, toolbar,
}: {
  rows: T[]
  columns: Column<T>[]
  filters?: Filter<T>[]
  getKey: (row: T) => string
  onRowClick?: (row: T) => void
  rowClass?: (row: T) => string
  searchPlaceholder?: string
  empty?: ReactNode
  pageSize?: number
  initialSort?: { key: string; dir: 'asc' | 'desc' }
  toolbar?: ReactNode
}) {
  const [q, setQ] = useState('')
  const [sort, setSort] = useState(initialSort)
  const [active, setActive] = useState<Record<string, string>>({})
  const [page, setPage] = useState(0)

  const valueOf = (row: T, col: Column<T>): string | number => {
    if (col.value) return col.value(row)
    const c = col.cell(row)
    return typeof c === 'string' || typeof c === 'number' ? c : ''
  }

  const filtered = useMemo(() => {
    const needle = q.trim().toLowerCase()
    return rows.filter(row => {
      for (const f of filters) {
        const v = active[f.key]
        if (v && !f.match(row, v)) return false
      }
      if (!needle) return true
      // Searched across every column's own value, so a search box needs no
      // explanation of which fields it covers — it covers what you can see.
      return columns.some(c => String(valueOf(row, c)).toLowerCase().includes(needle))
    })
  }, [rows, q, active, filters, columns])

  const sorted = useMemo(() => {
    if (!sort) return filtered
    const col = columns.find(c => c.key === sort.key)
    if (!col) return filtered
    const dir = sort.dir === 'asc' ? 1 : -1
    return [...filtered].sort((a, b) => {
      const av = valueOf(a, col), bv = valueOf(b, col)
      if (typeof av === 'number' && typeof bv === 'number') return (av - bv) * dir
      return String(av).localeCompare(String(bv), undefined, { numeric: true }) * dir
    })
  }, [filtered, sort, columns])

  const pages = Math.max(1, Math.ceil(sorted.length / pageSize))
  // A filter that empties the last page must not strand you on it. Clamped
  // DURING RENDER rather than corrected in an effect: setState in an effect
  // renders once with the wrong page and again with the right one, which shows
  // as a flash of an empty table.
  const current = Math.min(page, pages - 1)
  const shown = sorted.slice(current * pageSize, current * pageSize + pageSize)

  const toggleSort = (col: Column<T>) => {
    if (col.sortable === false) return
    setSort(s => s?.key === col.key
      ? { key: col.key, dir: s.dir === 'asc' ? 'desc' : 'asc' }
      : { key: col.key, dir: 'asc' })
  }

  return (
    <div className="dt">
      <div className="dt-bar">
        <div className="dt-search">
          <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
            <circle cx="7" cy="7" r="4.5" stroke="#6b7796" strokeWidth="1.5" />
            <path d="M10.5 10.5 14 14" stroke="#6b7796" strokeWidth="1.5" strokeLinecap="round" />
          </svg>
          <input value={q} placeholder={searchPlaceholder}
            onChange={e => { setQ(e.target.value); setPage(0) }} />
        </div>
        {filters.map(f => (
          <select key={f.key} className="dt-filter" value={active[f.key] || ''}
            onChange={e => { setActive({ ...active, [f.key]: e.target.value }); setPage(0) }}>
            <option value="">All {f.label.toLowerCase()}</option>
            {f.options.map(o => <option key={o} value={o}>{o}</option>)}
          </select>))}
        {toolbar}
        <span className="dt-count">
          {sorted.length === rows.length
            ? `${rows.length} ${rows.length === 1 ? 'item' : 'items'}`
            : `${sorted.length} of ${rows.length}`}
        </span>
      </div>

      <table className="dt-table">
        <thead>
          <tr>
            {columns.map(c => (
              <th key={c.key} style={{ width: c.width, textAlign: c.align }}
                className={`${c.sortable === false ? '' : 'sortable'} ${sort?.key === c.key ? 'sorted' : ''}`}
                onClick={() => toggleSort(c)}>
                {c.header}
                {c.sortable !== false && (
                  <span className="arrow">{sort?.key === c.key ? (sort.dir === 'asc' ? '▲' : '▼') : '↕'}</span>)}
              </th>))}
          </tr>
        </thead>
        <tbody>
          {shown.map(row => (
            <tr key={getKey(row)}
              className={`${onRowClick ? 'clickable' : ''} ${rowClass?.(row) || ''}`}
              onClick={onRowClick ? () => onRowClick(row) : undefined}>
              {columns.map(c => <td key={c.key} style={{ textAlign: c.align }}>{c.cell(row)}</td>)}
            </tr>))}
          {shown.length === 0 && (
            <tr><td colSpan={columns.length}><div className="dt-empty">
              {q || Object.values(active).some(Boolean) ? 'Nothing matches those filters.' : empty}
            </div></td></tr>)}
        </tbody>
      </table>

      {pages > 1 && (
        <div className="dt-pager">
          <span className="page-n">Page {current + 1} of {pages}</span>
          <button className="ghost small" disabled={current === 0} onClick={() => setPage(0)}>« First</button>
          <button className="ghost small" disabled={current === 0} onClick={() => setPage(current - 1)}>‹ Prev</button>
          <button className="ghost small" disabled={current >= pages - 1} onClick={() => setPage(current + 1)}>Next ›</button>
          <button className="ghost small" disabled={current >= pages - 1} onClick={() => setPage(pages - 1)}>Last »</button>
        </div>)}
    </div>
  )
}
