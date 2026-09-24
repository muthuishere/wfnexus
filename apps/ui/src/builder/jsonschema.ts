import type { JSONSchema } from '../api'

/** The text of every schema box on the page, and the parse error of each.
 *
 *  It lives above the editors, not inside them, so half-typed JSON survives
 *  switching to another step and back. The COMMITTED schema is only ever
 *  replaced by text that parses, so a stray keystroke can never wipe a step's
 *  contract — the worst an unparseable box can do is refuse to save. */
export type JsonSlots = {
  text: Record<string, string>
  errors: Record<string, string>
  setText: (slot: string, v: string) => void
  setError: (slot: string, msg: string) => void
}

/** Where a JSON.parse failure happened, as line/column a person can find.
 *  V8 reports a character offset; the line and column are what is on screen. */
export function whereFailed(text: string, message: string): string {
  const m = /at position (\d+)/.exec(message)
  if (!m) return message
  const pos = Math.min(Number(m[1]), text.length)
  const before = text.slice(0, pos)
  const line = before.split('\n').length
  const col = pos - before.lastIndexOf('\n')
  return `${message} — line ${line}, column ${col}`
}

/** parseSchema is the whole contract of the box: valid JSON, and an object. */
export function parseSchema(text: string): { schema?: JSONSchema; error?: string } {
  if (!text.trim()) return { error: 'empty — a schema is at least {}' }
  let parsed: unknown
  try {
    parsed = JSON.parse(text)
  } catch (e) {
    return { error: whereFailed(text, e instanceof Error ? e.message : String(e)) }
  }
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    return { error: `a JSON Schema is an object — this is a ${Array.isArray(parsed) ? 'list' : parsed === null ? 'null' : typeof parsed}` }
  }
  return { schema: parsed as JSONSchema }
}
