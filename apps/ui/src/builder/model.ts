import type { Budget, DecideGate, Gate, Guardrail, JSONSchema, Question, Step, TeamMember, WorkflowDraft } from '../api'

export const SCALAR_TYPES = ['string', 'number', 'integer', 'boolean', 'array', 'object'] as const
export type ScalarType = (typeof SCALAR_TYPES)[number]

export const emptyBudget = (): Budget => ({})
export const emptyGuardrail = (): Guardrail => ({ deny: 'bash', argsContain: [], reason: '' })
export const emptyTeamMember = (n: number): TeamMember => ({ id: `helper-${n}`, does: '', soul: '', skills: [], tools: [] })
export const emptyGate = (): Gate => ({ field: '', equals: false, action: 'needs_input', message: '' })
export const emptyDecideGate = (question: string): DecideGate => ({ question, atLeast: 0.6, action: 'fail', message: '' })
export const emptyQuestion = (type: Question['type']): Question =>
  type === 'choice' ? { type, instructions: '', options: { option_a: '', option_b: '' } }
    : type === 'score' ? { type, instructions: '', levels: ['', ''] }
      : { type: 'noul', instructions: '', true: '', false: '' }

export function blankStep(n: number): Step {
  return {
    id: `step-${n}`,
    name: `Step ${n}`,
    description: '',
    soul: '',
    prompt: '',
    skills: [],
    tools: [],
    outputSchema: {
      type: 'object',
      additionalProperties: false,
      required: ['summary'],
      properties: { summary: { type: 'string', description: 'one-paragraph account of what this step established' } },
    },
  }
}

/** The starting point for #/workflows/new: one step wired end to end, so the
 *  author edits a working thing instead of assembling one from nothing. */
export function templateDraft(firstSkill?: string, firstTool = 'read'): WorkflowDraft {
  const step = blankStep(1)
  step.id = 'investigate'
  step.name = 'Investigate'
  step.description = 'Read the repository and establish what is actually true.'
  step.soul = 'You are an engineer who reports only what the source shows, with file:line, and never speculates beyond it.'
  step.skills = firstSkill ? [firstSkill] : []
  step.tools = [firstTool]
  step.budget = { maxTurns: 20, maxToolCalls: 60, maxWallSec: 300 }
  step.prompt = 'Task:\n{{ .Input.task }}\n\nInspect the repository, establish the facts, then call submit_output.'
  step.outputSchema = {
    type: 'object',
    additionalProperties: false,
    required: ['found', 'summary'],
    properties: {
      found: { type: 'boolean', description: 'true if the repository confirms the task is actionable' },
      summary: { type: 'string', description: 'one paragraph of what the source actually shows' },
    },
  }
  return {
    name: '',
    description: '',
    inputSchema: {
      type: 'object',
      required: ['task'],
      properties: { task: { type: 'string', title: 'Task', format: 'textarea' } },
    },
    steps: [step],
  }
}

/** Strip the editor's empty scaffolding before PUTting, so the backend sees the
 *  same thing the YAML preview shows rather than a pile of blank keys. */
export function forSave(d: WorkflowDraft): WorkflowDraft {
  const trimList = (v?: string[]) => {
    const out = (v || []).map(s => s.trim()).filter(Boolean)
    return out.length ? out : undefined
  }
  const budget = (b?: Budget) => {
    if (!b) return undefined
    const e = Object.entries(b).filter(([, v]) => typeof v === 'number' && v > 0)
    return e.length ? (Object.fromEntries(e) as Budget) : undefined
  }
  return {
    name: d.name.trim(),
    description: d.description?.trim() || '',
    inputSchema: d.inputSchema,
    steps: d.steps.map(s => ({
      ...s,
      id: s.id.trim(),
      name: s.name?.trim() || s.id.trim(),
      description: s.description?.trim() || undefined,
      soul: s.soul?.trim() || undefined,
      model: s.model?.trim() || undefined,
      skills: trimList(s.skills) || [],
      tools: trimList(s.tools) || [],
      mcp: trimList(s.mcp),
      budget: budget(s.budget),
      team: s.team?.length
        ? s.team.map(m => ({ ...m, soul: m.soul?.trim() || undefined, model: m.model?.trim() || undefined, skills: trimList(m.skills), tools: trimList(m.tools), budget: budget(m.budget) }))
        : undefined,
      guardrails: s.guardrails?.length
        ? s.guardrails.map(g => ({ ...g, argsContain: trimList(g.argsContain) }))
        : undefined,
      gates: s.gates?.length ? s.gates : undefined,
      decide: s.decide,
      requiresApproval: s.requiresApproval || undefined,
      askHuman: s.askHuman || undefined,
      maxTurns: s.maxTurns || undefined,
      maxAttempts: s.maxAttempts || undefined,
      timeoutSec: s.timeoutSec || undefined,
    })),
  }
}

// ── immutable helpers ───────────────────────────────────────────────────────
export const replaceAt = <T,>(list: T[], i: number, v: T): T[] => list.map((x, j) => (j === i ? v : x))
export const removeAt = <T,>(list: T[], i: number): T[] => list.filter((_, j) => j !== i)
export const moveItem = <T,>(list: T[], i: number, delta: number): T[] => {
  const j = i + delta
  if (j < 0 || j >= list.length) return list
  const out = [...list]
  ;[out[i], out[j]] = [out[j], out[i]]
  return out
}
/** Rename a key while keeping insertion order — options and properties are
 *  ordered maps to the author even though JS objects only mostly are. */
export function renameKey<T>(obj: Record<string, T>, from: string, to: string): Record<string, T> {
  if (from === to) return obj
  const out: Record<string, T> = {}
  for (const [k, v] of Object.entries(obj)) out[k === from ? to : k] = v
  return out
}
export const schemaProps = (s?: JSONSchema): Array<[string, JSONSchema]> => Object.entries(s?.properties || {})
