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
  /** names a provider registry entry; empty ⇒ the process default */
  provider?: string
  /** a `run` step's shell: bash | sh | pwsh | powershell | cmd. empty ⇒ this machine's best */
  shell?: string
  /** deterministic node: a shell command instead of an agent */
  run?: string
  /** DAG edges — a step runs once every id here is done */
  needs?: string[]
  /** derived planning (ADR 0014): facts consumed and established */
  consumes?: string[]; produces?: string[]
  skills: string[]; tools: string[]; mcp?: string[]
  outputSchema: JSONSchema; requiresApproval?: boolean; gates?: Gate[]
  maxTurns?: number; maxAttempts?: number; timeoutSec?: number; model?: string
  // added by the agent-harness work — may be absent on an older API
  soul?: string; budget?: Budget; guardrails?: Guardrail[]; team?: TeamMember[]
  decide?: Decide; askHuman?: boolean
}
export type Workflow = { name: string; description: string; inputSchema: JSONSchema; steps: Step[]; path: string }
/** What the builder edits and PUTs — `path` is assigned by the API, not authored. */
export type WorkflowDraft = Omit<Workflow, 'path'> & { path?: string }

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

// ── the provider / classifier / mcp registries ─────────────────────────────
// Every entry is a NAME a step can use (ADR 0011). `apiKeyEnv` is the name of
// an environment variable and NEVER a value — the UI must never render or
// collect a secret into it.
export type ProviderKind = 'http' | 'cli' | 'acp'
export type Provider = {
  name: string; kind: ProviderKind; description?: string
  baseUrl?: string; style?: string; model?: string; apiKeyEnv?: string
  preset?: string; command?: string[]; args?: string[]; repairs?: number; timeoutSec?: number
}
export type Classifier = {
  name: string; description?: string; backend: string
  baseUrl?: string; model?: string; apiKeyEnv?: string
}
export type McpServer = { name: string; description?: string; command?: string; args?: string[]; url?: string }
export type Skipped = { location: string; reason: string }
/** Every registry endpoint answers this shape. `skipped` is what the loader refused. */
export type Registry<T> = { entries: T[] | null; skipped: Skipped[] | null }

/** What is actually wired on THIS machine, as opposed to what is declared. */
export type Doctor = {
  default: { model: string; baseUrl: string; style: string; apiKeyEnv: string; keySet: boolean }
  providers: DoctorEntry[]
  classifiers: DoctorEntry[]
  skills: { count: number; skipped?: string[] }
  mcp: { count: number }
  workflows: { count: number }
  models: string[]
  shell: { os: string; arch: string; using: string; path: string; problem?: string; available?: string[] }
  problems: string[]
}
export type DoctorEntry = {
  name: string; kind: string; model?: string; detail?: string; ready: boolean; problem?: string
}

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
  /** Create or replace a workflow. The API validates and 400s with {error} on rejection. */
  saveWorkflow: (name: string, definition: WorkflowDraft) =>
    j<Workflow>(fetch(`/api/workflows/${encodeURIComponent(name)}`, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ definition }),
    })),
  skills: () => j<SkillRegistry>(fetch('/api/skills')),
  deleteWorkflow: (name: string) =>
    j(fetch(`/api/workflows/${encodeURIComponent(name)}`, { method: 'DELETE' })),

  /** What is wired on this machine — the default model, every provider and
   *  classifier, the shell, and whether each could run right now. */
  doctor: () => j<Doctor>(fetch('/api/doctor')),
  models: () => j<string[]>(fetch('/api/models')),
  // These endpoints answer {entries, skipped} — a skipped entry is one the
  // loader REFUSED, and it is the more interesting half: it is why a name a
  // workflow uses is not available.
  providers: () => j<Registry<Provider>>(fetch('/api/providers')),
  classifiers: () => j<Registry<Classifier>>(fetch('/api/classifiers')),
  mcp: () => j<Registry<McpServer>>(fetch('/api/mcp')),
  saveProvider: (p: Provider) =>
    j<Provider>(fetch(`/api/providers/${encodeURIComponent(p.name)}`, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(p),
    })),
  deleteProvider: (name: string) =>
    j(fetch(`/api/providers/${encodeURIComponent(name)}`, { method: 'DELETE' })),
  saveClassifier: (c: Classifier) =>
    j<Classifier>(fetch(`/api/classifiers/${encodeURIComponent(c.name)}`, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(c),
    })),
  deleteClassifier: (name: string) =>
    j(fetch(`/api/classifiers/${encodeURIComponent(name)}`, { method: 'DELETE' })),
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
