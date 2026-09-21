// A YAML emitter for exactly the shape a workflow definition has: nested plain
// objects, arrays, strings, numbers and booleans. Deliberately not a general
// YAML library — the builder only ever serialises what it itself produced, and
// adding a dependency to pretty-print five node kinds is not worth it.
import type { WorkflowDraft, Step } from '../api'

const pad = (n: number) => ' '.repeat(n)
const plainKey = /^[A-Za-z_][A-Za-z0-9_.-]*$/
// Anything that could start a flow collection, a comment, an anchor, a tag or a
// number is quoted; so is a word YAML 1.1 would read as a boolean.
const needsQuote = /^$|^[\s>|@`%&*!?#-]|[:#]\s|[:#]$|^(true|false|yes|no|on|off|null|~)$|^[-+.]?\d|[\n"'{}[\],]|\s$/i

function scalar(v: unknown): string {
  if (v === null || v === undefined) return 'null'
  if (typeof v === 'boolean' || typeof v === 'number') return String(v)
  const s = String(v)
  return needsQuote.test(s) ? JSON.stringify(s) : s
}

function block(prefix: string, text: string, ind: number, out: string[]) {
  // `|-` keeps newlines and strips the trailing ones, which is what prompts want.
  out.push(`${prefix}|-`)
  for (const line of text.replace(/\n+$/, '').split('\n')) out.push(line ? pad(ind + 2) + line : '')
}

function entries(v: object): Array<[string, unknown]> {
  return Object.entries(v).filter(([, x]) => x !== undefined)
}

function dumpMap(obj: object, ind: number, out: string[]) {
  for (const [k, v] of entries(obj)) dumpEntry(k, v, ind, out)
}

function dumpEntry(k: string, v: unknown, ind: number, out: string[]) {
  const key = plainKey.test(k) ? k : JSON.stringify(k)
  const head = `${pad(ind)}${key}: `
  if (Array.isArray(v)) {
    if (v.length === 0) { out.push(`${head}[]`); return }
    out.push(`${pad(ind)}${key}:`)
    for (const item of v) dumpItem(item, ind + 2, out)
    return
  }
  if (v && typeof v === 'object') {
    if (entries(v).length === 0) { out.push(`${head}{}`); return }
    out.push(`${pad(ind)}${key}:`)
    dumpMap(v, ind + 2, out)
    return
  }
  if (typeof v === 'string' && v.includes('\n')) { block(head, v, ind, out); return }
  out.push(`${head}${scalar(v)}`)
}

function dumpItem(item: unknown, ind: number, out: string[]) {
  if (Array.isArray(item)) {
    out.push(`${pad(ind)}-`)
    for (const i of item) dumpItem(i, ind + 2, out)
    return
  }
  if (item && typeof item === 'object') {
    if (entries(item).length === 0) { out.push(`${pad(ind)}- {}`); return }
    const sub: string[] = []
    dumpMap(item, ind + 2, sub)
    out.push(`${pad(ind)}- ${sub[0].trimStart()}`)
    for (const l of sub.slice(1)) out.push(l)
    return
  }
  if (typeof item === 'string' && item.includes('\n')) { block(`${pad(ind)}- `, item, ind, out); return }
  out.push(`${pad(ind)}- ${scalar(item)}`)
}

/** Drops keys the loader would read as "present but empty".
 *  `false` and `0` are KEPT — `equals: false` is a real gate and dropping it
 *  would silently change the workflow's meaning. Call sites that mean
 *  "omit when unset" pass `|| undefined` themselves. */
function clean<T extends object>(o: T): Partial<T> {
  const out: Record<string, unknown> = {}
  for (const [k, v] of Object.entries(o)) {
    if (v === undefined || v === null) continue
    if (typeof v === 'string' && v.trim() === '') continue
    if (Array.isArray(v) && v.length === 0) continue
    if (!Array.isArray(v) && typeof v === 'object' && Object.keys(v).length === 0) continue
    out[k] = v
  }
  return out as Partial<T>
}
/** omit when the value is unset/zero/false */
const opt = <T,>(v: T | undefined): T | undefined => (v ? v : undefined)

function budgetDoc(b: Step['budget']) {
  if (!b) return undefined
  const d = clean({
    max_turns: opt(b.maxTurns), max_tokens: opt(b.maxTokens), max_tool_calls: opt(b.maxToolCalls),
    max_wall_sec: opt(b.maxWallSec), max_children: opt(b.maxChildren),
    max_concurrent: opt(b.maxConcurrent), max_depth: opt(b.maxDepth),
  })
  return Object.keys(d).length ? d : undefined
}

function decideDoc(d: NonNullable<Step['decide']>) {
  const questions: Record<string, unknown> = {}
  for (const [key, q] of Object.entries(d.questions || {})) {
    questions[key] = clean({
      type: q.type, instructions: q.instructions,
      options: q.options && Object.keys(q.options).length ? q.options : undefined,
      levels: q.levels, true: q.true, false: q.false,
    })
  }
  return clean({
    state: d.state,
    questions,
    gates: (d.gates || []).map(g => clean({
      question: g.question, below: g.below, at_least: g.atLeast, is: g.is,
      action: g.action, skip_to: g.skipTo, message: g.message,
    })),
  })
}

function stepDoc(s: Step) {
  return clean({
    id: s.id,
    name: s.name,
    description: s.description,
    requires_approval: opt(s.requiresApproval),
    ask_human: opt(s.askHuman),
    soul: s.soul,
    system: s.system,
    model: s.model,
    skills: s.skills,
    tools: s.tools,
    mcp: s.mcp,
    max_turns: opt(s.maxTurns),
    max_attempts: opt(s.maxAttempts),
    timeout_sec: opt(s.timeoutSec),
    budget: budgetDoc(s.budget),
    team: (s.team || []).map(m => clean({
      id: m.id, does: m.does, soul: m.soul, skills: m.skills,
      tools: m.tools, model: m.model, budget: budgetDoc(m.budget),
    })),
    guardrails: (s.guardrails || []).map(g => clean({
      deny: g.deny, args_contain: g.argsContain, reason: g.reason,
    })),
    decide: s.decide ? decideDoc(s.decide) : undefined,
    prompt: s.prompt,
    output_schema: s.outputSchema,
    gates: (s.gates || []).map(g => clean({
      field: g.field, equals: g.equals, action: g.action,
      skip_to: g.skipTo, message: g.message,
    })),
  })
}

export function toYaml(d: WorkflowDraft): string {
  const doc = clean({
    name: d.name,
    description: d.description,
    input_schema: d.inputSchema,
    steps: d.steps.map(stepDoc),
  })
  const out: string[] = []
  dumpMap(doc, 0, out)
  return out.join('\n') + '\n'
}
