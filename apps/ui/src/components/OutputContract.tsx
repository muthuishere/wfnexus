import type { JSONSchema } from '../api'

/** The step's output_schema, read at a glance: required fields first, in bold. */
export default function OutputContract({ schema }: { schema?: JSONSchema }) {
  const props = Object.entries(schema?.properties || {})
  if (props.length === 0) return <div className="muted">No output contract.</div>
  const required = new Set(schema?.required || [])
  const sorted = [...props].sort((a, b) => Number(required.has(b[0])) - Number(required.has(a[0])))
  return (
    <div className="contract">
      {sorted.map(([k, p]) => (
        <div className="field" key={k}>
          <span className={required.has(k) ? 'name req' : 'name'}>{k}</span>
          <span className="type mono">{typeLabel(p)}</span>
          <span className="muted">{p.description || ''}</span>
        </div>
      ))}
      {schema?.additionalProperties === false && <div className="muted" style={{ fontSize: 11, marginTop: 6 }}>closed object — no extra fields accepted</div>}
    </div>
  )
}

function typeLabel(p: JSONSchema): string {
  if (p.enum) return p.enum.join(' | ')
  if (p.type === 'array') return `${p.items?.type || 'any'}[]`
  return p.type || 'any'
}
