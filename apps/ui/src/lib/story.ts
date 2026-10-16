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
