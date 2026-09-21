import type { Decision, Question, Step } from '../api'

const pct = (n: number) => `${Math.round(n * 100)}%`

function Bars({ probs }: { probs: Record<string, number> }) {
  const rows = Object.entries(probs).sort((a, b) => b[1] - a[1])
  return (
    <div className="bars">
      {rows.map(([k, v]) => (
        <div className="bar" key={k}>
          <span className="lbl mono">{k}</span>
          <span className="track"><span className="fill" style={{ width: pct(Math.max(0, Math.min(1, v))) }} /></span>
          <span className="val mono">{pct(v)}</span>
        </div>))}
    </div>
  )
}

/** The judge tier's answer for a step. Advisory only — it authorises nothing. */
export default function DecisionPanel({ decision, step }: { decision?: Decision; step?: Step }) {
  const answers = Object.entries(decision?.answers || {})
  if (!decision || answers.length === 0) return null
  const qs = step?.decide?.questions || {}
  return (
    <div className="card decision">
      <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
        <h3 style={{ margin: 0 }}>Judge decision</h3>
        <span className="muted mono">{decision.model || 'model unknown'}</span>
        <span style={{ marginLeft: 'auto' }}>
          {decision.calibrated === true
            ? <span className="badge done">calibrated</span>
            : <span className="badge failed">not calibrated</span>}
        </span>
      </div>
      {decision.calibrated !== true && (
        <div className="banner warn" style={{ marginTop: 8 }}>
          These numbers are <b>not calibrated</b> on this backend — read them as a ranking, not a probability.
          A threshold tuned elsewhere does not transfer.
        </div>)}
      {answers.map(([key, a]) => {
        const q: Question | undefined = qs[key]
        return (
          <div className="answer" key={key}>
            <div className="ahead">
              <b className="mono">{key}</b>
              {q && <span className="badge pending">{q.type}</span>}
              <span className="verdict mono">
                {a.noul !== undefined && pct(a.noul)}
                {a.choice !== undefined && a.choice}
                {a.score !== undefined && String(a.score)}
              </span>
              {a.confidence !== undefined && <span className="muted">conf {pct(a.confidence)}</span>}
            </div>
            {q?.instructions && <div className="muted">{q.instructions}</div>}
            {a.choice !== undefined && q?.options?.[a.choice] && <div className="muted">→ {q.options[a.choice]}</div>}
            {a.probabilities && <Bars probs={a.probabilities} />}
            {a.reasoning && <div className="muted" style={{ marginTop: 4 }}>{a.reasoning}</div>}
          </div>)
      })}
    </div>
  )
}
