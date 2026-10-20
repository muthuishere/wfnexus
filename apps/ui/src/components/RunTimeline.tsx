import { useState } from 'react'
import type { Event, Step, StepRun } from '../api'
import { stepDecision } from '../lib/story'

// ── the run, drawn as time ───────────────────────────────────────────────────
// One row per step, its bar drawn to SCALE against the longest step in the run,
// so a 27-turn step visibly dwarfs a 4-turn one instead of both being a line in
// a table. Next to the name is what the step DECIDED (stepDecision in
// lib/story.ts); its status is an icon with a colour, and a label for a
// screen reader and on hover — colour alone is not a status.

export type Usage = {
  totalTokens?: number; promptTokens?: number; completionTokens?: number
  llmCalls?: number; toolCalls?: number; elapsedMs?: number
  costUsd?: number; costUnknown?: boolean; priceSource?: string
  models?: Record<string, { calls: number; promptTokens: number; completionTokens: number; ms: number }>
}

/** Milliseconds a step has been working. A running step is measured against
 *  `now`, which is why the page re-renders on a clock and not only on an event. */
export function elapsedMs(s: StepRun | undefined, now: number): number {
  const u = s?.usage as Usage | undefined
  if (!s?.startedAt) return 0
  const start = Date.parse(s.startedAt)
  const end = s.finishedAt ? Date.parse(s.finishedAt) : now
  const wall = end - start
  return wall > 0 ? wall : (u?.elapsedMs ?? 0)
}

export function dur(ms: number): string {
  if (ms <= 0) return '—'
  if (ms < 1000) return `${ms}ms`
  const s = ms / 1000
  if (s < 60) return `${s < 10 ? s.toFixed(1) : Math.round(s)}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ${Math.round(s - m * 60)}s`
  return `${Math.floor(m / 60)}h ${m % 60}m`
}

export function tokens(n?: number): string {
  if (!n) return '0'
  if (n < 1000) return String(n)
  if (n < 1_000_000) return `${(n / 1000).toFixed(n < 10_000 ? 1 : 0)}k`
  return `${(n / 1e6).toFixed(2)}M`
}

/** THE money distinction, and the most product-specific thing on this page.
 *
 *  A local CLI or a local model bills nothing, and that TRUE `$0.00` is a
 *  feature — the number a machine you own can show and a hosted product
 *  cannot. It must never look like the absence of a number. So three states
 *  are rendered three ways (ADR 0020):
 *
 *    costUsd: 0        → "$0.00 free"      — known, and worth noticing
 *    costUsd: 4.37     → "$4.37"
 *    costUnknown: true → "cost unknown"    — a price nobody configured
 *    neither key       → nothing at all     — a script step, or a run older than cost accounting
 */
export function money(n: number): string {
  if (n === 0) return '$0.00'
  if (n < 0.01) return `$${n.toFixed(4)}`
  return `$${n.toFixed(2)}`
}

export function Cost({ usage, big }: { usage?: Usage; big?: boolean }) {
  const c = big ? 'cost big' : 'cost'
  // No cost recorded (a script step, or a run older than cost accounting):
  // say nothing. A label for an absent number is noise on every row.
  if (!usage || (usage.costUsd === undefined && !usage.costUnknown)) return null
  if (usage.costUnknown)
    return <span className={`${c} unknown`} title="this provider has no price in the registry — the tokens are real, the price is not known">cost unknown</span>
  const n = usage.costUsd ?? 0
  if (n === 0)
    return <span className={`${c} free`} title="a model on this machine — nothing was billed">{money(0)} <i>free</i></span>
  // `default` is the seed list price, not what this account pays: say so.
  if (usage.priceSource === 'default')
    return <span className={c} title="approximate — no model family matched, so the fallback (*) price was charged; set one in System → Model prices">≈{money(n)}</span>
  return <span className={c}>{money(n)}</span>
}

/** A rejected `submit_output` and the correction that followed it: the product
 *  working, which in a flat log is indistinguishable from noise. */
export type Correction = { id: number; step: string; why: string; fixedTurn?: number }

export function correctionsOf(events: Event[]): Correction[] {
  const out: Correction[] = []
  for (let i = 0; i < events.length; i++) {
    const e = events[i]
    if (e.kind !== 'tool_result' || e.payload?.name !== 'submit_output' || !e.payload?.isError) continue
    const fix = events.slice(i + 1).find(x =>
      x.stepId === e.stepId && x.kind === 'tool_call' && x.payload?.name === 'submit_output')
    out.push({ id: e.id, step: e.stepId, why: String(e.payload?.output ?? ''), fixedTurn: fix?.payload?.turn })
  }
  return out
}

export function Corrections({ items }: { items: Correction[] }) {
  if (!items.length) return null
  return (
    <div className="corrections">
      {items.map(c => (
        <div key={c.id} className="correction">
          <b>output rejected</b>
          <span>{c.fixedTurn === undefined
            ? ' — the step never produced a valid answer'
            : ` — the model corrected itself on turn ${c.fixedTurn}`}</span>
          <pre>{c.why}</pre>
        </div>))}
    </div>
  )
}

const ICON: Record<string, string> = {
  done: '✓', approved: '✓', failed: '✕', rejected: '✕', cancelled: '✕', skipped: '↷',
  running: '●', queued: '○', awaiting_approval: '⏸', needs_input: '?', pending: '○',
}

type Row = { def: Step; run?: StepRun; ms: number; ceiling: number }

/** A step that needed no model: no turns, tokens or cost to report. */
const isScriptStep = (d: Step) => !!d.run || !!(d as any).judge

export default function RunTimeline({ steps, byId, events, now, selected, onPick, skipReason, input }: {
  steps: Step[]; byId: Record<string, StepRun>; events: Event[]
  now: number; selected: string; onPick: (id: string) => void
  /** why the skipped steps were not needed — shown on their collapsed line */
  skipReason?: string
  /** the run's input, which `input.*` guards read */
  input?: unknown
}) {
  // facts mode: the planner routes, so a skip is "not taken", never a guard
  const planned = !steps.some(s => s.needs?.length) && steps.some(s => s.produces?.length || s.consumes?.length)
  const [showSkipped, setShowSkipped] = useState(false)
  const all: Row[] = steps.map(def => ({
    def, run: byId[def.id], ms: elapsedMs(byId[def.id], now),
    ceiling: def.budget?.maxTurns || def.maxTurns || 0,
  }))
  const skipped = all.filter(r => r.run?.status === 'skipped')
  // Skipped steps are folded into ONE line: thirteen "skipped" rows bury the
  // three that did something.
  const rows = showSkipped ? all : all.filter(r => r.run?.status !== 'skipped')
  const max = Math.max(1, ...all.map(r => r.ms))
  const corrections = correctionsOf(events)

  return (
    <div className="tl">
      {rows.map(r => {
        const i = all.indexOf(r)
        const st = r.run?.status || 'pending'
        const u = r.run?.usage as Usage | undefined
        const running = st === 'running'
        const script = isScriptStep(r.def)
        const turns = r.run?.turns ?? 0
        const mine = corrections.filter(c => c.step === r.def.id)
        const dec = stepDecision(r.def, r.run, byId, { input, planned })
        return (
          <div key={r.def.id} className={`tlrow ${st}${selected === r.def.id ? ' on' : ''}`}
            onClick={() => onPick(r.def.id)}>
            <div className="tlname">
              <span className={`tlicon ${st}`} role="img" aria-label={st.replace(/_/g, ' ')}
                title={st.replace(/_/g, ' ')}>{ICON[st] || '·'}</span>
              <b title={r.def.name || r.def.id}>{i + 1}. {r.def.name || r.def.id}</b>
              {r.def.requiresApproval && <span className="badge awaiting_approval">gate</span>}
            </div>
            <div className={`tldecision tone-${dec.tone}`} title={dec.text}>{dec.text}</div>
            <div className="tlbar" title={`${dur(r.ms)} of ${dur(max)}`}>
              <i className={st} style={{ width: `${Math.max(r.ms > 0 ? 2 : 0, (r.ms / max) * 100)}%` }} />
            </div>
            <div className="tlfacts">
              <span className="t">{r.ms > 0 ? dur(r.ms) : '—'}</span>
              {/* Script steps: status and duration only. Agent steps: the
                  model facts, each one only when there is one to show. */}
              {!script && turns > 0 && <span>{turns}{r.ceiling ? `/${r.ceiling}` : ''} turns</span>}
              {!script && !!u?.totalTokens && <span>{tokens(u.totalTokens)} tok</span>}
              {!script && <Cost usage={u} />}
            </div>
            {running && !script && (
              <div className="tlprog">
                {r.ceiling > 0 && (
                  <span className="meter" aria-label={`turn ${r.run?.turns ?? 0} of ${r.ceiling}`}>
                    <i style={{ width: `${Math.min(100, ((r.run?.turns ?? 0) / r.ceiling) * 100)}%` }} />
                  </span>)}
                <span className="muted">
                  working · {u?.llmCalls ?? 0} model calls, {u?.toolCalls ?? 0} tool calls
                </span>
              </div>)}
            {mine.length > 0 && <Corrections items={mine} />}
          </div>)
      })}
      {skipped.length > 0 && (
        <button type="button" className="tlskipped" onClick={() => setShowSkipped(v => !v)}
          aria-expanded={showSkipped}>
          {showSkipped ? '▾' : '▸'} {skipped.length} step{skipped.length === 1 ? '' : 's'} not needed
          {skipReason && <span className="muted"> — {skipReason}</span>}
        </button>)}
    </div>
  )
}
