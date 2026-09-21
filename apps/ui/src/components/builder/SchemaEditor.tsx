import { useState } from 'react'
import type { JSONSchema } from '../../api'
import { renameKey, SCALAR_TYPES, schemaProps } from '../../builder/model'

/** The output schema becomes the `submit_output` tool's input schema, so every
 *  property here is a field the agent is forced to produce — and a `description`
 *  is the only thing telling it what to put there. */
export default function SchemaEditor({ schema, onChange, label, hint }: {
  schema: JSONSchema; onChange: (s: JSONSchema) => void; label: string; hint?: string
}) {
  const [raw, setRaw] = useState<string | null>(null)
  const [rawErr, setRawErr] = useState('')
  const props = schemaProps(schema)
  const required = schema.required || []

  const setProp = (name: string, p: JSONSchema) =>
    onChange({ ...schema, properties: { ...(schema.properties || {}), [name]: p } })

  const rename = (from: string, to: string) => {
    if (!to || (to !== from && schema.properties?.[to])) return
    onChange({
      ...schema,
      properties: renameKey(schema.properties || {}, from, to),
      required: required.map(r => (r === from ? to : r)),
    })
  }

  const remove = (name: string) => {
    const next = { ...(schema.properties || {}) }
    delete next[name]
    onChange({ ...schema, properties: next, required: required.filter(r => r !== name) })
  }

  const toggleRequired = (name: string) =>
    onChange({ ...schema, required: required.includes(name) ? required.filter(r => r !== name) : [...required, name] })

  const add = () => {
    let n = 1
    while (schema.properties?.[`field_${n}`]) n++
    setProp(`field_${n}`, { type: 'string', description: '' })
  }

  if (raw !== null) {
    return (
      <div className="schemaed">
        <div className="sehead">
          <b>{label}</b>
          <span className="muted">raw JSON schema</span>
          <button type="button" className="ghost small" onClick={() => {
            try {
              const parsed: unknown = JSON.parse(raw)
              if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error('the schema must be a JSON object')
              onChange(parsed as JSONSchema); setRaw(null); setRawErr('')
            } catch (e) { setRawErr(e instanceof Error ? e.message : String(e)) }
          }}>Apply</button>
          <button type="button" className="ghost small" onClick={() => { setRaw(null); setRawErr('') }}>Discard</button>
        </div>
        <textarea style={{ minHeight: 220 }} value={raw} onChange={e => setRaw(e.target.value)} spellCheck={false} />
        {rawErr
          ? <div className="issue error"><span className="mark">✕</span>{rawErr}</div>
          : <div className="hint">Anything valid JSON Schema is accepted here; the form editor covers the common subset and will show what it can.</div>}
      </div>
    )
  }

  return (
    <div className="schemaed">
      <div className="sehead">
        <b>{label}</b>
        <span className="muted">{props.length} field{props.length === 1 ? '' : 's'} · {required.length} required</span>
        <button type="button" className="ghost small" onClick={() => setRaw(JSON.stringify(schema, null, 2))}>Edit as JSON</button>
        <button type="button" className="ghost small" onClick={add}>+ field</button>
      </div>
      {hint && <div className="hint">{hint}</div>}
      {props.length === 0 && <div className="muted" style={{ padding: '8px 0' }}>No fields yet — an agent with nothing to submit can never finish this step.</div>}
      {props.map(([name, p]) => (
        <div className="serow" key={name}>
          <input className="mono" value={name} onChange={e => rename(name, e.target.value)} />
          <select value={p.type || 'string'} onChange={e => setProp(name, { ...p, type: e.target.value })}>
            {SCALAR_TYPES.map(t => <option key={t}>{t}</option>)}
          </select>
          <input value={p.description || ''} placeholder="what the agent must put here" onChange={e => setProp(name, { ...p, description: e.target.value })} />
          <label className="req" title="required">
            <input type="checkbox" checked={required.includes(name)} onChange={() => toggleRequired(name)} /> req
          </label>
          <button type="button" className="ghost small" onClick={() => remove(name)}>×</button>
          <div className="seextra">
            {p.type === 'array' && (
              <label className="inline">items
                <select value={p.items?.type || 'string'} onChange={e => setProp(name, { ...p, items: { ...(p.items || {}), type: e.target.value } })}>
                  {SCALAR_TYPES.map(t => <option key={t}>{t}</option>)}
                </select>
              </label>)}
            {(p.type === 'string' || p.type === undefined) && (
              <label className="inline">enum
                <input className="mono" placeholder="comma,separated (optional)" value={(p.enum || []).join(',')}
                  onChange={e => {
                    const v = e.target.value.split(',').map(s => s.trim()).filter(Boolean)
                    setProp(name, { ...p, enum: v.length ? v : undefined })
                  }} />
              </label>)}
          </div>
        </div>))}
      <label className="inline" style={{ marginTop: 8 }}>
        <input type="checkbox" checked={schema.additionalProperties === false}
          onChange={e => onChange({ ...schema, additionalProperties: e.target.checked ? false : undefined })} />
        reject fields not listed here (<span className="mono">additionalProperties: false</span>)
      </label>
    </div>
  )
}
