// A line-by-line mirror of apps/api/internal/workflow/workflow.go —
// validate(), validateTeam(), validateGuardrails(), validateDecide(), degenerate().
//
// The backend rejects the WHOLE file on the first failure, so an author who
// only learns about a problem from the save response gets one error at a time
// out of a definition that may have a dozen. Everything checkable client-side
// is therefore checked here, all at once, and attributed to the field it came
// from. What we CANNOT check here is anything needing the catalog interplay the
// server has (skills/tools existence is checked against the live registries we
// fetched, which is the closest we get).
import type { Question, Step, WorkflowDraft } from '../api'

export type Issue = {
  /** which step this belongs to, or -1 for the workflow itself */
  step: number
  /** a stable key the editor uses to put the message next to the input */
  field: string
  message: string
  severity: 'error' | 'warn'
}

const QUESTION_TYPES = ['noul', 'choice', 'score']
const GATE_ACTIONS = ['needs_input', 'fail', 'skip_to']

/** degenerate() from workflow.go: every value empty, every value equal to its
 *  own key, or every value identical. Fewer than 2 options can't be degenerate. */
export function degenerate(options: Record<string, string>): boolean {
  const e = Object.entries(options)
  if (e.length < 2) return false
  let allEmpty = true, allSelf = true, allSame = true, first = ''
  for (const [k, v] of e) {
    if (v.trim() !== '') allEmpty = false
    if (v !== k) allSelf = false
    if (first === '') first = v
    else if (v !== first) allSame = false
  }
  return allEmpty || allSelf || allSame
}

/** Which of the three degeneracy shapes tripped, in the author's words. */
export function degeneracyReason(options: Record<string, string>): string {
  const e = Object.entries(options)
  if (e.length < 2) return ''
  if (e.every(([, v]) => v.trim() === '')) return 'every option is blank'
  if (e.every(([k, v]) => v === k)) return 'every option just repeats its own id'
  if (e.every(([, v]) => v === e[0][1])) return 'every option has the same text'
  return ''
}

function questionIssues(key: string, q: Question, i: number, push: (f: string, m: string, s?: Issue['severity']) => void) {
  const at = `decide.questions.${key}`
  if (!q.instructions?.trim()) push(`${at}.instructions`, `question “${key}” needs instructions — the classifier reads nothing else about what you are asking.`)
  if (!QUESTION_TYPES.includes(q.type)) {
    push(`${at}.type`, `question “${key}” has unknown type “${q.type}” — must be noul, choice or score.`)
    return
  }
  if (q.type === 'choice') {
    const opts = q.options || {}
    const n = Object.keys(opts).length
    if (n < 1 || n > 255) push(`${at}.options`, `question “${key}” needs 1..255 options, got ${n}.`)
    for (const k of Object.keys(opts)) if (!k.trim()) push(`${at}.options`, `question “${key}” has an option with a blank id.`)
    if (degenerate(opts)) {
      push(`${at}.options`, `question “${key}”: ${degeneracyReason(opts)}. An option described only by its id ranks at chance — write what PICKING IT would mean.`)
    } else if (n >= 2 && Object.values(opts).some(v => !v.trim())) {
      push(`${at}.options`, `question “${key}” has a blank option description — that option can only be picked by luck.`, 'warn')
    }
  }
  if (q.type === 'score') {
    const n = q.levels?.length ?? 0
    if (n < 2 || n > 10) push(`${at}.levels`, `question “${key}” needs an ordered rubric of 2..10 levels, got ${n}. Level 0 is the first one.`)
    if (q.levels?.some(l => !l.trim())) push(`${at}.levels`, `question “${key}” has a blank rubric level — the model cannot tell it from its neighbours.`)
  }
  void i
}

function stepIssues(s: Step, i: number, allIds: string[], seenBefore: Set<string>, out: Issue[]) {
  const push = (field: string, message: string, severity: Issue['severity'] = 'error') =>
    out.push({ step: i, field, message, severity })

  // ── validate(): id, prompt, output_schema, uniqueness ────────────────────
  if (!s.id?.trim()) push('id', 'a step needs an id — it is how later prompts reference this step’s output.')
  else if (!/^[A-Za-z0-9_-]+$/.test(s.id)) push('id', 'step ids should be letters, digits, - or _ so `{{ .Steps.<id> }}` resolves.', 'warn')
  if (seenBefore.has(s.id)) push('id', `duplicate step id “${s.id}”.`)
  if (!s.prompt?.trim()) push('prompt', 'a step needs a prompt — this is the task handed to the agent.')
  if (!s.outputSchema || Object.keys(s.outputSchema).length === 0) {
    push('outputSchema', 'a step needs an output_schema — it becomes the submit_output tool the agent must call to finish.')
  } else if (!s.outputSchema.properties || Object.keys(s.outputSchema.properties).length === 0) {
    push('outputSchema', 'the output schema has no properties — the agent has nothing to submit and the step can never complete.', 'warn')
  }
  if (!s.skills?.length && !s.tools?.length) {
    push('tools', 'this step grants no skills and no tools — the agent can only talk, not act.', 'warn')
  }

  // ── validateTeam() ───────────────────────────────────────────────────────
  const teamSeen = new Set<string>()
  for (const [mi, m] of (s.team || []).entries()) {
    if (!m.id?.trim() || !m.does?.trim()) push(`team.${mi}`, `team member ${mi + 1} needs an id and a \`does\` — \`does\` is the routing description the delegating model reads.`)
    if (m.id && teamSeen.has(m.id)) push(`team.${mi}`, `duplicate team member “${m.id}”.`)
    if (m.id) teamSeen.add(m.id)
    if (m.id && m.id === s.id) push(`team.${mi}`, 'a team member may not share the step’s id.')
    if (m.id && !m.tools?.length && !m.skills?.length) push(`team.${mi}`, `“${m.id}” has no tools or skills, so delegating to it can only produce prose.`, 'warn')
  }
  if ((s.team || []).length && (s.tools || []).includes('bash')) {
    push('team', 'this step holds `bash`, so it can grep and cat for itself — delegation is strictly more expensive than doing the work, and measured runs show it ignores the team. A team is only structurally enforced when the parent lacks the capability.', 'warn')
  }

  // ── validateGuardrails() ─────────────────────────────────────────────────
  for (const [gi, g] of (s.guardrails || []).entries()) {
    if (!g.deny?.trim()) push(`guardrails.${gi}.deny`, `guardrail ${gi + 1} needs \`deny\` — a tool name, or * for any.`)
    if (!g.reason?.trim()) push(`guardrails.${gi}.reason`, `guardrail on “${g.deny || '?'}” needs a reason — the model is shown it verbatim as the tool result, so it is the only thing that tells it what to do instead.`)
  }

  // ── validateDecide() ─────────────────────────────────────────────────────
  if (s.decide) {
    const qs = s.decide.questions || {}
    if (!Object.keys(qs).length) push('decide', 'decide needs at least one question.')
    if (!s.decide.state?.trim()) push('decide.state', 'decide needs a `state` — the text the classifier judges. With none, every question is answered on nothing.', 'warn')
    for (const [key, q] of Object.entries(qs)) questionIssues(key, q, i, push)
    for (const [gi, g] of (s.decide.gates || []).entries()) {
      const at = `decide.gates.${gi}`
      if (!qs[g.question]) push(at, `decide gate ${gi + 1} references unknown question “${g.question || '—'}”.`)
      const set = [g.below !== undefined, g.atLeast !== undefined, !!g.is].filter(Boolean).length
      if (set !== 1) push(at, `decide gate on “${g.question || '—'}” needs exactly one of below / at_least / is (has ${set}).`)
      if (!GATE_ACTIONS.includes(g.action)) push(at, `decide gate has unknown action “${g.action || '—'}” — needs_input, fail or skip_to.`)
      if (g.action === 'skip_to' && !g.skipTo) push(at, 'a skip_to decide gate needs skip_to.')
      if (g.action === 'skip_to' && g.skipTo && !allIds.includes(g.skipTo)) push(at, `skip_to names “${g.skipTo}”, which is not a step in this workflow.`)
      if (g.is && qs[g.question]?.type === 'choice' && !(qs[g.question].options || {})[g.is]) {
        push(at, `“${g.is}” is not an option of question “${g.question}”.`)
      }
    }
  }

  // ── gates ────────────────────────────────────────────────────────────────
  for (const [gi, g] of (s.gates || []).entries()) {
    const at = `gates.${gi}`
    if (!g.field?.trim()) push(at, `gate ${gi + 1} needs a field — a top-level key of this step’s output.`)
    else if (s.outputSchema?.properties && !s.outputSchema.properties[g.field]) {
      push(at, `gate ${gi + 1} watches “${g.field}”, which this step’s output schema does not declare.`, 'warn')
    }
    if (!GATE_ACTIONS.includes(g.action)) push(at, `gate ${gi + 1} has unknown action “${g.action || '—'}” — needs_input, fail or skip_to.`)
    if (g.action === 'skip_to') {
      if (!g.skipTo) push(at, `gate ${gi + 1} is a skip_to gate and needs skip_to.`)
      else if (!allIds.includes(g.skipTo)) push(at, `skip_to names “${g.skipTo}”, which is not a step in this workflow.`)
    }
  }
}

export function validateDraft(d: WorkflowDraft, known?: { skills: Set<string>; tools: Set<string> }): Issue[] {
  const out: Issue[] = []
  if (!d.name?.trim()) out.push({ step: -1, field: 'name', message: 'a workflow needs a name — it is also the filename and the URL.', severity: 'error' })
  else if (!/^[a-z0-9][a-z0-9-]*$/.test(d.name)) out.push({ step: -1, field: 'name', message: 'use a lowercase, hyphenated name (it becomes workflows/<name>.yaml).', severity: 'warn' })
  if (!d.steps.length) out.push({ step: -1, field: 'steps', message: 'a workflow with no steps does nothing.', severity: 'error' })

  const allIds = d.steps.map(s => s.id)
  const seen = new Set<string>()
  for (const [i, s] of d.steps.entries()) {
    stepIssues(s, i, allIds, seen, out)
    if (s.id) seen.add(s.id)
    if (!known) continue
    for (const name of s.skills || []) if (!known.skills.has(name)) out.push({ step: i, field: 'skills', message: `skill “${name}” is not in the registry — the backend refuses the whole file for this.`, severity: 'error' })
    for (const name of s.tools || []) if (!known.tools.has(name)) out.push({ step: i, field: 'tools', message: `“${name}” is not a toolnexus built-in.`, severity: 'error' })
    for (const m of s.team || []) {
      for (const name of m.skills || []) if (!known.skills.has(name)) out.push({ step: i, field: 'team', message: `${m.id}: skill “${name}” is not in the registry.`, severity: 'error' })
      for (const name of m.tools || []) if (!known.tools.has(name)) out.push({ step: i, field: 'team', message: `${m.id}: “${name}” is not a built-in tool.`, severity: 'error' })
    }
  }
  return out
}

export const errorsOnly = (issues: Issue[]) => issues.filter(i => i.severity === 'error')
export const issuesFor = (issues: Issue[], step: number, prefix: string) =>
  issues.filter(i => i.step === step && (i.field === prefix || i.field.startsWith(prefix + '.')))
