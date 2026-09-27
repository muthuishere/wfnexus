/** Where you are: Projects / project / workflow. Every level is a link back up,
 *  which is the whole of the navigation below the top bar. */
export default function Crumbs({ items }: { items: Array<[label: string, href: string]> }) {
  return (
    <nav className="crumbs" aria-label="Breadcrumb">
      {items.map(([label, to], i) => (
        <span key={to}>
          {i > 0 && <span className="sep">/</span>}
          <a href={to} className={i > 0 ? 'mono' : ''}>{label}</a>
        </span>))}
    </nav>
  )
}

/** Tabs within one place. The selection lives in the page, not the address:
 *  a tab is a view of the same thing, not somewhere else. */
export function Tabs<T extends string>({ tabs, value, onChange }: {
  tabs: Array<{ key: T; label: string; count?: number }>; value: T; onChange: (t: T) => void
}) {
  return (
    <div className="tabs" role="tablist">
      {tabs.map(t => (
        <button key={t.key} role="tab" aria-selected={t.key === value}
          className={t.key === value ? 'active' : ''} onClick={() => onChange(t.key)}>
          {t.label}{t.count !== undefined && <span className="tabcount">{t.count}</span>}
        </button>))}
    </div>
  )
}
