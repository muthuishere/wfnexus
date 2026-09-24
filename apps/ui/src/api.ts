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
  /** the runner label this step is placed on; empty ⇒ the platform itself */
  runsOn?: string
  /** environment for this step, layered over its job's and the workflow's.
   *  A value may NAME a variable (`${GITHUB_PAT}`) instead of holding one —
   *  that is how a credential reaches a step without entering the file. */
  env?: Record<string, string>
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
/** `on:` — what may start a workflow. Actions' own block (see triggers.go). */
export type Triggers = {
  dispatch?: boolean
  schedule?: Array<{ cron: string; input?: Record<string, unknown> }>
  repositoryDispatch?: { types?: string[] } | null
  workflowCall?: boolean
}
export type Workflow = {
  name: string; description: string; inputSchema: JSONSchema; steps: Step[]; path: string
  /** Workflow-level env. It cascades into every step, which is why the builder
   *  shows it beside the step's own — the two are one table with an order. */
  env?: Record<string, string>
  /** Attached folders, one line each: HOST[:AT][:ro]. */
  mount?: string[]
  /** The files that sit beside this workflow on disk. Read-only here for now:
   *  the Builder lists them so a save cannot silently drop them. */
  files?: Array<{ path: string; mode?: number; body?: string }>
  on?: Triggers
  goal?: string
  maxParallel?: number
  /** which source this came from — "local", or a repository imported with `wfx import` */
  source?: string
  repoDir?: string
}
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

export type Run = { id: string; project: string; workflow: string; status: string; input: any; currentStep: string; error: string; createdAt: string; updatedAt: string }
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

/** A PROJECT is a repository the platform knows about, and it owns its
 *  workflows and their runs: project → workflow → runs, the same hierarchy
 *  GitHub Actions has. `local` is the platform's own workflows directory. */
export type Project = {
  name: string; dir: string; repo?: string; url?: string; local: boolean
  workflows: string[]
  runs: number
  lastRun?: string
  lastRunAt?: string
  problems?: Skipped[]
}

/** What is actually wired on THIS machine, as opposed to what is declared. */
export type Doctor = {
  default: { model: string; baseUrl: string; style: string; apiKeyEnv: string; keySet: boolean }
  providers: DoctorEntry[]
  classifiers: DoctorEntry[]
  storage: { driver: string; artifacts: string }
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

/** A machine that has joined the pool. It is known by its LABELS; the platform
 *  never holds its address, because the worker connects out. */
export type Worker = {
  id: string; name: string; labels: string[]; os: string; arch: string
  version: string; status: 'online' | 'offline' | string; lastSeen: string; createdAt: string
}
export type Pool = {
  workers: Worker[]
  /** Labels the platform serves in its own process — these need no machine. */
  localLabels: string[]
  joinCommand: string
  url: string
}

/** A workflow that exists to be copied. Not a second kind of file: it loads,
 *  validates and dry-runs like any other, so a template that would not run is
 *  caught by the loader rather than by the first person who uses it. */
export type TemplatePhase = {
  id: string; name?: string; kind: string; description?: string
  skills?: string[]; tools?: string[]; approval?: boolean
}
export type Template = {
  name: string; title: string; summary: string; description?: string
  fill?: string[]; source?: string; phases: TemplatePhase[]
  /** true when a phase has no skills yet — the normal state of a template. */
  needsSkills: boolean
}

/** One entry of the platform's env store. A secret arrives with NO value: the
 *  only path a value takes out of the database is into the process that runs a
 *  step. A non-secret is ordinary configuration and is shown. */
export type EnvVar = {
  scope: string; scopeName?: string; key: string; secret: boolean
  value?: string; updatedAt: string
}
export type EnvList = {
  scope: string; scopeName: string; vars: EnvVar[]
  /** where the encryption key came from — a variable name or a path, never the key */
  keySource: string
}

export const api = {
  workflows: () => j<Workflow[]>(fetch('/api/workflows')),
  workflow: (name: string) => j<Workflow>(fetch(`/api/workflows/${name}`)),
  reload: () => j<Workflow[]>(post('/api/workflows/reload')),
  /** The authoritative verdict, with no side effect: the loader, the catalog
   *  and the JSON-schema compiler, all of which live on the server. The
   *  builder mirrors what it can in TypeScript; this is the truth. */
  validateWorkflow: (definition: WorkflowDraft) =>
    j<{ valid: boolean; error?: string }>(post('/api/workflows/validate', { definition })),
  /** Create or replace a workflow. The API validates and 400s with {error} on rejection. */
  saveWorkflow: (name: string, definition: WorkflowDraft) =>
    j<Workflow>(fetch(`/api/workflows/${encodeURIComponent(name)}`, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ definition }),
    })),
  skills: () => j<SkillRegistry>(fetch('/api/skills')),

  /** The env store: system-wide, and per project. Values go in; only names
   *  come back for anything marked secret. */
  env: (project?: string) => j<EnvList>(fetch(project ? `/api/projects/${encodeURIComponent(project)}/env` : '/api/env')),
  setEnv: (v: { key: string; value: string; secret: boolean }, project?: string) =>
    j<{ ok: boolean }>(fetch(project ? `/api/projects/${encodeURIComponent(project)}/env` : '/api/env', {
      method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(v),
    })),
  deleteEnv: (key: string, project?: string) =>
    j(fetch(project
      ? `/api/projects/${encodeURIComponent(project)}/env/${encodeURIComponent(key)}`
      : `/api/env/${encodeURIComponent(key)}`, { method: 'DELETE' })),

  /** The template gallery, and copying one into a workflow of your own. */
  templates: () => j<Template[]>(fetch('/api/templates')),
  template: (name: string) => j<Workflow>(fetch(`/api/templates/${encodeURIComponent(name)}`)),
  copyTemplate: (name: string, as: string) =>
    j<{ name: string; path: string }>(post(`/api/templates/${encodeURIComponent(name)}/copy`, { as })),

  /** The worker pool, and the one command that adds a machine to it. */
  workers: () => j<Pool>(fetch('/api/workers')),
  rotateWorkerToken: () => j<{ token: string; joinCommand: string }>(post('/api/workers/token/rotate')),
  removeWorker: (id: string) => j(fetch(`/api/workers/${encodeURIComponent(id)}`, { method: 'DELETE' })),
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
  projects: () => j<Project[]>(fetch('/api/projects')),
  project: (name: string) => j<Project>(fetch(`/api/projects/${encodeURIComponent(name)}`)),
  addProject: (body: { repo: string; name?: string; branch?: string }) =>
    j<Project>(post('/api/projects', body)),
  removeProject: (name: string) =>
    j(fetch(`/api/projects/${encodeURIComponent(name)}`, { method: 'DELETE' })),

  /** Runs, narrowed by the two axes of the hierarchy. */
  runs: (q?: { project?: string; workflow?: string }) => {
    const p = new URLSearchParams()
    if (q?.project) p.set('project', q.project)
    if (q?.workflow) p.set('workflow', q.workflow)
    return j<Run[]>(fetch('/api/runs' + (p.toString() ? `?${p}` : '')))
  },
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
