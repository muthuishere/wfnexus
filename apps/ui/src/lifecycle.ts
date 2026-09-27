// The workflow LIFECYCLE API, in one place: onboarding a repo, proposals
// (every create/edit/delete of a workflow is a proposed change on a branch, not
// a write), the classifier's review of a proposal, and drift (workflows on disk
// that no commit tracks).
//
// This API is still landing on the server. Every shape and path it depends on
// is here and nowhere else, so when the server settles, this file is the one
// that moves.

export type ProposalKind = 'create' | 'edit' | 'delete'
export type ProposalStatus = 'pending' | 'approved' | 'rejected' | 'merged'
export type Verdict = 'approve' | 'reject' | 'unreviewed'

export type Review = { verdict: Verdict; reason: string; score: number }

export type Proposal = {
  id: string
  project: string
  workflow: string
  kind: ProposalKind
  branch: string
  pr_url?: string
  status: ProposalStatus
  review: Review
  created_at?: string
}
/** GET /api/proposals/{id} adds the change itself. */
export type ProposalDetail = Proposal & { diff: string }

/** A workflow file present on disk that no commit tracks. */
export type DriftEntry = { workflow: string; path: string; status?: string }

/** What connecting a repo answers with: the project, and the workflows it
 *  already had. None means "offer the starter templates". */
export type ConnectResult = { name: string; workflows?: string[] }

const e = encodeURIComponent

async function j<T>(r: Promise<Response>): Promise<T> {
  const res = await r
  if (!res.ok) {
    const message = (await res.json().catch(() => ({}))).error || res.statusText
    throw new Error(message)
  }
  return res.json()
}
const post = (url: string, body?: unknown) =>
  fetch(url, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body ?? {}) })

// Who decides — the same actor the run approve/reject sends (api.ts).
function actor(): string {
  try { return localStorage.getItem('wfx.actor') || 'ui' } catch { return 'ui' }
}

/** The server's proposal (camelCase, flat review fields). */
type Wire = {
  id: string; project: string; workflow: string; kind: ProposalKind; branch: string
  prUrl?: string; status: ProposalStatus; reviewVerdict?: Verdict; reviewReason?: string
  reviewScore?: number; createdAt?: string
}
const fromWire = (w: Wire): Proposal => ({
  id: w.id, project: w.project, workflow: w.workflow, kind: w.kind, branch: w.branch,
  pr_url: w.prUrl || undefined, status: w.status, created_at: w.createdAt,
  review: { verdict: w.reviewVerdict || 'unreviewed', reason: w.reviewReason || '', score: w.reviewScore ?? 0 },
})

/** The proposal inside a save/delete/copy answer, or undefined when the
 *  server wrote the workflow directly — so the page says "proposed", never
 *  "saved", only when it really is a proposal. */
export function asProposal(x: unknown): Proposal | undefined {
  const w = (x as { proposal?: Wire } | null)?.proposal
  return w && typeof w === 'object' && 'branch' in w ? fromWire(w) : undefined
}

/** "PR #12" from a GitHub-style URL; the branch when there is no PR yet. */
export function prLabel(p: Pick<Proposal, 'pr_url' | 'branch'>): string {
  const n = p.pr_url?.match(/\/pull\/(\d+)/)?.[1] || p.pr_url?.match(/merge_requests\/(\d+)/)?.[1]
  if (n) return `PR #${n}`
  return p.pr_url ? 'PR' : p.branch
}

type Drift = { project: string; workflow: string; kind: string; files: string[] }

export const lifecycle = {
  /** Onboard a repository by its git URL (cloned once, on `branch`). */
  connect: (body: { url: string; branch?: string }) =>
    j<ConnectResult>(post('/api/projects', { repo: body.url, branch: body.branch || undefined })),

  proposals: (project: string, workflow?: string) =>
    j<Wire[]>(fetch(`/api/proposals?project=${e(project)}${workflow ? `&workflow=${e(workflow)}` : ''}`))
      .then(ws => (ws || []).map(fromWire)),
  proposal: (id: string) =>
    j<{ proposal: Wire; diff: string }>(fetch(`/api/proposals/${e(id)}`))
      .then((r): ProposalDetail => ({ ...fromWire(r.proposal), diff: r.diff })),
  approve: (id: string) =>
    j<{ proposal: Wire }>(post(`/api/proposals/${e(id)}/approve`, { actor: actor() })).then(r => fromWire(r.proposal)),
  reject: (id: string, reason: string) =>
    j<{ proposal: Wire }>(post(`/api/proposals/${e(id)}/reject`, { reason, actor: actor() })).then(r => fromWire(r.proposal)),

  /** Deleting a workflow is a proposal too. */
  proposeDelete: (project: string, workflow: string) =>
    j<unknown>(fetch(`/api/workflows/${e(workflow)}?project=${e(project)}`, { method: 'DELETE' })).then(r => {
      const p = asProposal(r)
      if (!p) throw new Error('deleted directly — this project is not a git repository')
      return p
    }),

  drift: (project: string) =>
    j<Drift[]>(fetch(`/api/proposals/drift?project=${e(project)}`)).then(ds =>
      (ds || []).map((d): DriftEntry => ({ workflow: d.workflow, path: d.files.join(', '), status: d.kind }))),
  /** One proposal per drifted workflow — each is reviewed on its own. */
  proposeDrift: (project: string, workflow: string) =>
    j<{ proposal: Wire }>(post('/api/proposals/drift', { project, workflow, actor: actor() })).then(r => fromWire(r.proposal)),
}
