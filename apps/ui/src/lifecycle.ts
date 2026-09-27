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

/** True when a save/delete answered with a proposal rather than the old
 *  "written" workflow — so the page says "proposed", never "saved". */
export function isProposal(x: unknown): x is Proposal {
  return !!x && typeof x === 'object' && 'kind' in x && 'status' in x && 'branch' in x
}

/** "PR #12" from a GitHub-style URL; the branch when there is no PR yet. */
export function prLabel(p: Pick<Proposal, 'pr_url' | 'branch'>): string {
  const n = p.pr_url?.match(/\/pull\/(\d+)/)?.[1] || p.pr_url?.match(/merge_requests\/(\d+)/)?.[1]
  if (n) return `PR #${n}`
  return p.pr_url ? 'PR' : p.branch
}

export const lifecycle = {
  /** Onboard a repository by its git URL (cloned once, on `branch`). */
  connect: (body: { url: string; branch?: string }) =>
    j<ConnectResult>(post('/api/projects', { repo: body.url, branch: body.branch || undefined })),

  proposals: (project: string) => j<Proposal[]>(fetch(`/api/projects/${e(project)}/proposals`)),
  proposal: (id: string) => j<ProposalDetail>(fetch(`/api/proposals/${e(id)}`)),
  approve: (id: string) => j<Proposal>(post(`/api/proposals/${e(id)}/approve`)),
  reject: (id: string, reason: string) => j<Proposal>(post(`/api/proposals/${e(id)}/reject`, { reason })),

  /** Deleting a workflow is a proposal too. */
  proposeDelete: (project: string, workflow: string) =>
    j<Proposal>(fetch(`/api/workflows/${e(workflow)}?project=${e(project)}`, { method: 'DELETE' })),

  drift: (project: string) => j<DriftEntry[]>(fetch(`/api/projects/${e(project)}/drift`)),
  proposeDrift: (project: string) => j<Proposal>(post(`/api/projects/${e(project)}/drift/propose`)),
}
