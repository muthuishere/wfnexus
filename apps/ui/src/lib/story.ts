import type { Run, Step, StepRun } from '../api'

// ── the run tells its story ─────────────────────────────────────────────────
// A step writes markdown to $WFX_STEP_SUMMARY and one line
// `ok|incident|waiting|failed: <text>` to $WFX_OUTCOME (engine/story.go). They
// arrive as the step output's `summary` / `outcome`, and the run's verdict as
// `run.outcome`. An agent step's output fields of the same names count too.

export type Verdict = 'ok' | 'incident' | 'waiting' | 'failed' | 'running'
export type Headline = { verdict: Verdict; text: string }

const VERDICTS = ['ok', 'incident', 'waiting', 'failed']
const PAUSED = ['awaiting_approval', 'needs_input']

export function parseOutcome(s?: string): Headline | undefined {
  const line = (s || '').trim()
  if (!line) return undefined
  const i = line.indexOf(':')
  const head = (i < 0 ? line : line.slice(0, i)).trim().toLowerCase()
  if (!VERDICTS.includes(head)) return { verdict: 'ok', text: line }
  return { verdict: head as Verdict, text: i < 0 ? '' : line.slice(i + 1).trim() }
}

/** The run's headline: what it found, in one sentence. A live or paused run
 *  says so first — an old verdict must not read as the current one. */
export function headlineOf(run: Run): Headline {
  const oc = parseOutcome(run.outcome)
  if (PAUSED.includes(run.status))
    return { verdict: 'waiting', text: oc?.text || (run.status === 'awaiting_approval' ? 'waiting for an approval' : 'waiting for an answer') }
  if (run.status === 'running' || run.status === 'queued')
    return { verdict: 'running', text: oc?.text || (run.status === 'queued' ? 'queued' : `working${run.currentStep ? ` · ${run.currentStep}` : ''}`) }
  if (oc) return { ...oc, text: oc.text || defaultText(oc.verdict) }
  if (run.status === 'failed') return { verdict: 'failed', text: run.error?.split('\n')[0] || 'failed' }
  if (run.status === 'cancelled') return { verdict: 'failed', text: 'cancelled' }
  return { verdict: 'ok', text: 'completed' }
}

function defaultText(v: Verdict): string {
  return v === 'ok' ? 'all clear' : v
}

export function summaryOf(s?: StepRun): string {
  const v = s?.output?.summary
  return typeof v === 'string' ? v.trim() : ''
}

/** A script (`run:`) or judge step: no model, so no turns, tokens or cost. */
export const isScript = (d?: Step) => !!d && (!!d.run || !!(d as any).judge)

/** "16:45" in the viewer's clock. */
export const hhmm = (iso?: string | null) =>
  iso ? new Date(iso).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : ''

// ── each step's decision, in one line ───────────────────────────────────────
// A green "done" says nothing; what the step DECIDED or FOUND does. Read from
// what the step recorded, in this order — the same facts the run headline is
// made of, so a row and the headline can never disagree:
//
//   outcome   (WFX_OUTCOME / an `outcome` field)      "incident: 2 findings — …"
//   decision  (a judge's calibrated answers)          "actionable: work_it 64%"
//   summary   (WFX_STEP_SUMMARY's first real line)    "work gate: both agree"
//   output    (an agent's typed fields, short ones)   "change_type=config · needs_pr=false"
//   exit code (a script with nothing else to say)     "exit 0 · ops: /home/…"
//
// A skipped step says which `when:` did not hold, and what the value was.

export type Tone = Verdict | 'skipped' | 'neutral'
export type StepDecision = { tone: Tone; text: string }

type Guard = { path: string; equals?: unknown; exists?: boolean }

export function stepDecision(def: Step, run: StepRun | undefined, byId: Record<string, StepRun>): StepDecision {
  const st = run?.status || 'pending'
  if (st === 'skipped') return { tone: 'skipped', text: skipWhy(def, byId) }
  if (st === 'pending') return { tone: 'neutral', text: '' }
  if (st === 'running' || st === 'queued') return { tone: 'running', text: st === 'queued' ? 'queued' : 'working…' }
  if (st === 'awaiting_approval' || st === 'needs_input') {
    const msg = firstLine(run?.output?.message) || firstLine(run?.error)
    return { tone: 'waiting', text: msg || (st === 'awaiting_approval' ? 'waiting for an approval' : 'waiting for an answer') }
  }
  if (run?.resolution) return { tone: run.resolution === 'rejected' ? 'failed' : 'ok',
    text: `${run.resolution}${run.resolvedBy ? ` by ${run.resolvedBy}` : ''}${run.resolutionReason ? ` — ${run.resolutionReason}` : ''}` }
  if (st === 'failed' || st === 'cancelled') return { tone: 'failed', text: firstLine(run?.error) || st }

  const out = run?.output
  const oc = typeof out?.outcome === 'string' ? parseOutcome(out.outcome) : undefined
  if (oc) return { tone: oc.verdict, text: oc.text || oc.verdict }

  const answers = run?.decision?.answers
  if (answers && Object.keys(answers).length) {
    return { tone: 'neutral', text: Object.entries(answers).map(([k, a]) => {
      const v = a.choice ?? a.score ?? (a.noul !== undefined ? String(a.noul) : '?')
      return `${k}: ${v}${typeof a.confidence === 'number' ? ` ${Math.round(a.confidence * 100)}%` : ''}`
    }).join(' · ') }
  }

  const sm = summaryHeadline(summaryOf(run))
  if (sm) return { tone: 'neutral', text: sm }

  if (out && typeof out === 'object' && !Array.isArray(out)) {
    if (typeof out.exitCode === 'number') {
      const line = firstLine(out.stdout) || firstLine(out.stderr)
      return { tone: 'neutral', text: `exit ${out.exitCode}${line ? ` · ${line}` : ''}` }
    }
    const fields = Object.entries(out)
      .filter(([, v]) => typeof v === 'boolean' || typeof v === 'number' || (typeof v === 'string' && v.length <= 30 && !v.includes('\n')))
      .slice(0, 5).map(([k, v]) => `${k}=${v}`)
    if (fields.length) return { tone: 'neutral', text: fields.join(' · ') }
  }
  return { tone: 'ok', text: 'done' }
}

/** The first line of a step summary worth reading: past headings that only
 *  repeat the step's name; a table reads as its first row, plus how many more. */
export function summaryHeadline(md: string): string {
  const lines = md.split('\n').map(l => l.trim()).filter(Boolean)
  const body = lines.filter(l => !/^#{1,6}\s/.test(l))
  const first = body[0]
  if (!first) return lines[0] ? plain(lines[0].replace(/^#+\s*/, '')) : ''
  if (first.startsWith('|')) {
    const rows = body.filter(l => l.startsWith('|')).slice(1).filter(l => !/^\|[\s:|-]+\|$/.test(l))
    if (!rows.length) return 'no rows'
    const cells = rows[0].split('|').map(c => c.trim()).filter(c => c && c !== '-')
    return plain(cells.slice(0, 2).join(' · ')) + (rows.length > 1 ? ` (+${rows.length - 1} more)` : '')
  }
  // "**notify via whatsapp:**" announces the next line; say both
  return plain(first).endsWith(':') && body[1] ? `${plain(first)} ${plain(body[1])}` : plain(first)
}

function skipWhy(def: Step, byId: Record<string, StepRun>): string {
  const guards: Guard[] = (def as any).when || []
  const failed = guards.find(g => !holds(g, byId))
  const g = failed || guards[0]
  if (!g) return 'not needed'
  const [step, ...rest] = g.path.split('.')
  const want = g.exists !== undefined ? (g.exists ? 'exists' : 'absent') : `= ${JSON.stringify(g.equals)}`
  const up = byId[step]
  const was = !up || up.status === 'skipped' || up.status === 'pending' ? `${step} did not run`
    : `was ${JSON.stringify(dig(up.output, rest))}`
  return `needs ${g.path} ${want} (${was})`
}

function holds(g: Guard, byId: Record<string, StepRun>): boolean {
  const [step, ...rest] = g.path.split('.')
  const v = dig(byId[step]?.output, rest)
  if (g.exists !== undefined) return (v !== undefined && v !== null) === g.exists
  return JSON.stringify(v) === JSON.stringify(g.equals)
}

function dig(o: any, path: string[]): unknown {
  return path.reduce((v, k) => (v && typeof v === 'object' ? v[k] : undefined), o)
}

/** markdown emphasis and code ticks off; the words stay */
function plain(s: string): string {
  return s.replace(/^[>*\-•\s]+/, '').replace(/\*\*|__|`/g, '').replace(/^_(.*)_$/, '$1').trim()
}

function firstLine(s?: unknown): string {
  return typeof s === 'string' ? (s.split('\n').find(l => l.trim()) || '').trim() : ''
}
