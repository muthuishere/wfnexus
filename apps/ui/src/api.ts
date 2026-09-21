export type Step = {
  id: string; name: string; description?: string; skills: string[]; tools: string[]; mcp?: string[]
  outputSchema: any; requiresApproval?: boolean; gates?: any[]; maxTurns?: number; maxAttempts?: number; model?: string
}
export type Workflow = { name: string; description: string; inputSchema: any; steps: Step[]; path: string }
export type Run = { id: string; workflow: string; status: string; input: any; currentStep: string; error: string; createdAt: string; updatedAt: string }
export type StepRun = {
  id: string; runId: string; stepId: string; position: number; status: string; attempts: number; turns: number
  prompt: string; output: any; rawText: string; error: string; usage: any; startedAt?: string; finishedAt?: string
}
export type Artifact = { id: string; runId: string; stepId: string; name: string; contentType: string; sizeBytes: number; createdAt: string }
export type RunDetail = { run: Run; steps: StepRun[]; artifacts: Artifact[]; definition: Workflow }
export type Event = { id: number; runId: string; stepId: string; kind: string; payload: any; createdAt: string }

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
    for (const k of ['run.status', 'step.status', 'llm', 'tool_call', 'tool_result', 'log', 'error', 'metric', 'artifact'])
      es.addEventListener(k, (m: any) => onEvent(JSON.parse(m.data)))
    return () => es.close()
  },
}
