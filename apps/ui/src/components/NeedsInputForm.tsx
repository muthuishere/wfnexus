import { useState } from 'react'
import type { Question, StepRun } from '../api'

type Field = { key: string; label: string; help?: string; options?: string[] }

/** Derive the form from whatever the halt carried. Three shapes, in order of
 *  richness — typed questions, a plain missing_info list, or nothing at all. */
function fieldsFor(stepRun?: StepRun): Field[] {
  const out = stepRun?.output as { questions?: Record<string, Question>; missing_info?: unknown } | undefined
  const typed = out?.questions
  if (typed && typeof typed === 'object') {
    return Object.entries(typed).map(([key, q]) => ({
      key, label: q?.instructions || key, help: q?.type ? `expects: ${q.type}` : undefined,
      options: q?.options ? Object.keys(q.options) : q?.levels,
    }))
  }
  const missing = out?.missing_info
  if (Array.isArray(missing)) return missing.filter((m): m is string => typeof m === 'string').map(m => ({ key: m, label: m }))
  return []
}

/** The second of the human's two buttons. The halt is durable — this answer can
 *  arrive hours later, in another process, and the run picks up from it. */
export default function NeedsInputForm({ stepId, reason, stepRun, onSubmit }: {
  stepId: string; reason?: string; stepRun?: StepRun
  onSubmit: (extraContext: string) => void
}) {
  const fields = fieldsFor(stepRun)
  const [answers, setAnswers] = useState<Record<string, string>>({})
  const [extra, setExtra] = useState('')
  const set = (k: string, v: string) => setAnswers(a => ({ ...a, [k]: v }))

  const body = [
    ...fields.map(f => (answers[f.key]?.trim() ? `${f.label}\n→ ${answers[f.key].trim()}` : '')),
    extra.trim(),
  ].filter(Boolean).join('\n\n')

  return (
    <div className="banner warn hitl">
      <div className="hitlhead">
        <span className="badge needs_input">needs input</span>
        <b>{stepId} stopped and asked you a question</b>
      </div>
      {reason && <p>{reason}</p>}
      <p className="muted">
        {fields.length > 0
          ? 'Answer what you can — everything you write is appended to the run\'s extra context and the step re-runs with it.'
          : 'No structured questions came with this halt. Anything you write is appended to the run\'s extra context and the step re-runs with it.'}
      </p>
      {fields.map(f => (
        <div key={f.key}>
          <label>{f.label}{f.help && <span className="muted"> · {f.help}</span>}</label>
          {f.options?.length
            ? <select value={answers[f.key] ?? ''} onChange={e => set(f.key, e.target.value)}>
                <option value="">—</option>{f.options.map(o => <option key={o}>{o}</option>)}
              </select>
            : <input value={answers[f.key] ?? ''} onChange={e => set(f.key, e.target.value)} />}
        </div>))}
      <label>Extra context {fields.length > 0 && <span className="muted">· optional</span>}</label>
      <textarea value={extra} placeholder="logs, versions, anything the agent could not see…" onChange={e => setExtra(e.target.value)} />
      <div className="actions">
        <button disabled={!body} onClick={() => onSubmit(body)}>Submit &amp; continue</button>
      </div>
    </div>
  )
}
