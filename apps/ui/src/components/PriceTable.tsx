import { useCallback, useEffect, useState } from 'react'
import { api, type ModelPrice } from '../api'
import DataTable, { type Column } from './DataTable'

/** The price table (ADR 0020): what each model family costs per million tokens.
 *
 *  Every row starts as an APPROXIMATE list price shipped with the server, so a
 *  run shows a cost from its first step. Edit a row to what your account pays;
 *  removing a built-in family puts it back at its approximate default. A model
 *  id is matched by its longest family prefix — `claude-sonnet` prices
 *  `anthropic/claude-sonnet-4.5` — and `*` is charged when nothing matches. */
export default function PriceTable() {
  const [rows, setRows] = useState<ModelPrice[]>()
  const [err, setErr] = useState('')
  const [edit, setEdit] = useState<{ model: string; in: string; out: string; isNew?: boolean }>()

  const load = useCallback(() => {
    api.prices().then(r => { setRows(r.prices); setErr('') })
      .catch(e => setErr(e instanceof Error ? e.message : String(e)))
  }, [])
  useEffect(load, [load])

  const fail = (e: unknown) => setErr(e instanceof Error ? e.message : String(e))
  const save = () => {
    if (!edit) return
    const i = Number(edit.in), o = Number(edit.out)
    if (!edit.model.trim() || edit.in === '' || edit.out === '' || !(i >= 0) || !(o >= 0)) {
      setErr('A price needs a model and two numbers, 0 or more.'); return
    }
    api.setPrice({ model: edit.model.trim(), in: i, out: o }).then(() => { setEdit(undefined); load() }).catch(fail)
  }

  const columns: Column<ModelPrice>[] = [
    {
      key: 'model', header: 'Model family', width: 220, value: r => r.model,
      cell: r => <span className="mono" style={{ fontWeight: 550 }}>{r.model === '*' ? '* (fallback)' : r.model}</span>,
    },
    { key: 'in', header: 'In $/1M', width: 100, align: 'right', value: r => r.in, cell: r => <span className="mono">{r.in}</span> },
    { key: 'out', header: 'Out $/1M', width: 100, align: 'right', value: r => r.out, cell: r => <span className="mono">{r.out}</span> },
    {
      key: 'seeded', header: 'Source', width: 110, value: r => (r.seeded ? 'approx' : 'set'),
      cell: r => r.seeded
        ? <span className="muted" title="the approximate list price shipped with the server">≈ approx</span>
        : <span className="pill">set</span>,
    },
    {
      key: 'actions', header: '', width: 150, align: 'right', sortable: false,
      cell: r => (<>
        <button className="ghost small" onClick={() => setEdit({ model: r.model, in: String(r.in), out: String(r.out) })}>Edit</button>
        {!r.seeded && (
          <button className="ghost small danger-text" onClick={() => {
            if (!confirm(`Remove ${r.model}? A built-in family goes back to its approximate price.`)) return
            api.deletePrice(r.model).then(load).catch(fail)
          }}>Reset</button>)}
      </>),
    },
  ]

  return (
    <div className="card">
      <div className="subhead">
        <h2>Model prices</h2>
        <button className="ghost small" onClick={() => setEdit(edit ? undefined : { model: '', in: '', out: '', isNew: true })}>
          {edit ? 'Cancel' : '+ model'}
        </button>
      </div>
      <p className="muted" style={{ marginBottom: 12 }}>
        What a run is charged, in USD per million tokens. Rows marked ≈ are approximate list prices
        shipped with the server — set what your account actually pays. A model id matches its longest
        family prefix; <span className="mono">*</span> is used when nothing matches. Local programs
        and <span className="mono">localhost</span> endpoints are always $0. Also: <span className="mono">wfx prices</span>.
      </p>
      {err && <div className="banner err">{err}</div>}
      {edit && (
        <div style={{ display: 'flex', gap: 8, alignItems: 'center', marginBottom: 12, flexWrap: 'wrap' }}>
          <input className="mono" style={{ width: 220 }} placeholder="model family, e.g. claude-sonnet" value={edit.model}
            readOnly={!edit.isNew} onChange={e => setEdit({ ...edit, model: e.target.value })} />
          <input type="number" step="any" min="0" style={{ width: 100 }} placeholder="in $/1M" value={edit.in}
            onChange={e => setEdit({ ...edit, in: e.target.value })} />
          <input type="number" step="any" min="0" style={{ width: 100 }} placeholder="out $/1M" value={edit.out}
            onChange={e => setEdit({ ...edit, out: e.target.value })} />
          <button onClick={save}>Save price</button>
        </div>)}
      {!rows ? <div className="muted">loading…</div>
        : <DataTable rows={rows} columns={columns} getKey={r => r.model} pageSize={15}
          initialSort={{ key: 'model', dir: 'asc' }} searchPlaceholder="Search models…" empty="No prices." />}
    </div>
  )
}
