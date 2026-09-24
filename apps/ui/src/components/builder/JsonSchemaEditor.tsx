import { useEffect } from 'react'
import type { JSONSchema } from '../../api'
import { parseSchema, type JsonSlots } from '../../builder/jsonschema'

/** A JSON Schema, edited as JSON. One textarea, one error line, nothing else.
 *
 *  The field-by-field editor this replaces put a dozen controls on screen per
 *  property, wrapped into an unaligned column, with no way to tell which
 *  property a control belonged to. The schema IS JSON; showing it as JSON is
 *  smaller and strictly more capable, and the server compiles what is typed
 *  here with the same compiler the run loop uses.
 *
 *  Never destructive: the committed schema is only replaced by text that
 *  parses. While the box does not parse, the step keeps its last valid
 *  contract and the page refuses to save. */
export default function JsonSchemaEditor({ slot, schema, onChange, slots }: {
  slot: string
  schema?: JSONSchema
  onChange: (s: JSONSchema) => void
  slots: JsonSlots
}) {
  const text = slots.text[slot] ?? JSON.stringify(schema ?? {}, null, 2)

  // Debounced, so the error does not flash on every keystroke of a string
  // that is only briefly unfinished.
  useEffect(() => {
    const t = setTimeout(() => {
      const { schema: next, error } = parseSchema(text)
      slots.setError(slot, error || '')
      if (next && JSON.stringify(next) !== JSON.stringify(schema ?? {})) onChange(next)
    }, 200)
    return () => clearTimeout(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [text, slot])

  return (
    <div className="jsoned">
      <textarea
        className="mono" spellCheck={false} aria-label={slot} value={text}
        onChange={e => slots.setText(slot, e.target.value)}
        onKeyDown={e => {
          if (e.key !== 'Tab' || e.shiftKey) return
          e.preventDefault()
          const el = e.currentTarget
          const a = el.selectionStart, b = el.selectionEnd
          slots.setText(slot, text.slice(0, a) + '  ' + text.slice(b))
          requestAnimationFrame(() => { el.selectionStart = el.selectionEnd = a + 2 })
        }} />
      {slots.errors[slot] && <div className="issue error"><span className="mark">✕</span>{slots.errors[slot]}</div>}
    </div>
  )
}
