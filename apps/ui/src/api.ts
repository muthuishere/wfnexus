// ── the step harness ────────────────────────────────────────────────────────
// A step is a whole agent. These mirror apps/api/internal/workflow types.
// Fields marked optional are either genuinely optional in the YAML or are still
// landing in the API — every consumer must read them defensively.
export type Budget = {
  maxTurns?: number; maxTokens?: number; maxToolCalls?: number; maxWallSec?: number
  maxChildren?: number; maxConcurrent?: number; maxDepth?: number
}
export type Guardrail = { deny: string; argsContain?: string[]; reason: string }
export type TeamMember = {
  id: string; does: string; soul?: string; skills?: string[]; tools?: string[]; model?: string; budget?: Budget
}
export type QuestionType = 'noul' | 'choice' | 'score' | string
export type Question = {
  type: QuestionType; instructions: string
  options?: Record<string, string>   // choice: id → description-by-consequence
  levels?: string[]                  // score: ordered rubric, low → high
  true?: string; false?: string      // noul: what each pole means
}
export type DecideGate = {
  question: string; below?: number; atLeast?: number; is?: string
  action: string; skipTo?: string; message?: string
}
export type Decide = { state: string; questions: Record<string, Question>; gates?: DecideGate[] }
export type Gate = { field: string; equals: unknown; action: string; skipTo?: string; message?: string }

export type JSONSchema = {
  type?: string; title?: string; description?: string; format?: string; default?: unknown
  enum?: string[]; required?: string[]; properties?: Record<string, JSONSchema>
  items?: JSONSchema; additionalProperties?: boolean | JSONSchema
}

export type Step = {
  id: string; name: string; description?: string; prompt?: string; system?: string
  skills: string[]; tools: string[]; mcp?: string[]
  outputSchema: JSONSchema; requiresApproval?: boolean; gates?: Gate[]
  maxTurns?: number; maxAttempts?: number; timeoutSec?: number; model?: string
  // added by the agent-harness work — may be absent on an older API
  soul?: string; budget?: Budget; guardrails?: Guardrail[]; team?: TeamMember[]
  decide?: Decide; askHuman?: boolean
}
export type Workflow = { name: string; description: string; inputSchema: JSONSchema; steps: Step[]; path: string }

// ── the judge tier ──────────────────────────────────────────────────────────
// tn.Classifier output, attached to a step record once the API persists it.
export type DecisionAnswer = {
  noul?: number; choice?: string; score?: string | number
  probabilities?: Record<string, number>; confidence?: number; reasoning?: string
}
export type Decision = {
  answers?: Record<string, DecisionAnswer>; calibrated?: boolean; model?: string
}

export type Run = { id: string; workflow: string; status: string; input: any; currentStep: string; error: string; createdAt: string; updatedAt: string }
export type StepRun = {
  id: string; runId: string; stepId: string; position: number; status: string; attempts: number; turns: number
  prompt: string; output: any; rawText: string; error: string; usage: any; startedAt?: string; finishedAt?: string
  decision?: Decision
}
export type Artifact = { id: string; runId: string; stepId: string; name: string; contentType: string; sizeBytes: number; createdAt: string }
export type RunDetail = { run: Run; steps: StepRun[]; artifacts: Artifact[]; definition: Workflow }
export type Event = { id: number; runId: string; stepId: string; kind: string; payload: any; createdAt: string }

// ── the registries a step draws from ────────────────────────────────────────
export type SkillSource = 'project' | 'claude' | 'agents' | string
export type Skill = {
  name: string; description: string; root: string; location: string
  source: SkillSource
  /** locations of same-named skills lower in precedence that this one hides */
  shadowed?: string[]
}
export type SkippedSkill = { location: string; reason: string }
/** roots are in precedence order: earlier wins, later is shadowed */
export type SkillRegistry = { roots: string[]; skills: Skill[]; skipped: SkippedSkill[] }
export type BuiltinTool = { name: string; description: string }

async function j<T>(r: Promise<Response>): Promise<T> {
  const res = await r
  if (!res.ok) throw new Error((await res.json().catch(() => ({}))).error || res.statusText)
  return res.json()
}
const post = (url: string, body?: any) =>
  fetch(url, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body ?? {}) })

export const api = {
  workflows: () => j<Workflow[]>(fetch('/api/workflows')),
  workflow: (name: string) => j<Workflow>(fetch(`/api/workflows/${name}`)),
  reload: () => j<Workflow[]>(post('/api/workflows/reload')),
  skills: () => j<SkillRegistry>(fetch('/api/skills')),
  tools: () => j<BuiltinTool[]>(fetch('/api/tools')),
  runs: () => j<Run[]>(fetch('/api/runs')),
  run: (id: string) => j<RunDetail>(fetch(`/api/runs/${id}`)),
  createRun: (wf: string, input: any) => j<Run>(post(`/api/workflows/${wf}/runs`, input)),
  approve: (id: string, stepId: string) => j(post(`/api/runs/${id}/approve`, { stepId })),
  reject: (id: string, stepId: string, reason: string) => j(post(`/api/runs/${id}/reject`, { stepId, reason })),
  input: (id: string, input: any) => j(post(`/api/runs/${id}/input`, { input })),
  retry: (id: string, stepId: string) => j(post(`/api/runs/${id}/retry`, { stepId })),
  cancel: (id: string) => j(post(`/api/runs/${id}/cancel`)),
  events: (id: string, after: number, onEvent: (e: Event) => void) => {
    const es = new EventSource(`/api/runs/${id}/events?after=${after}`)
    es.onmessage = (m) => onEvent(JSON.parse(m.data))
    for (const k of ['run.status', 'step.status', 'llm', 'tool_call', 'tool_result', 'log', 'error', 'metric', 'artifact', 'decision'])
      es.addEventListener(k, (m: any) => onEvent(JSON.parse(m.data)))
    return () => es.close()
  },
}
