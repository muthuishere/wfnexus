import type { Decide, Question } from '../../api'
import { emptyDecideGate, emptyQuestion, removeAt, renameKey, replaceAt } from '../../builder/model'
import { degeneracyReason, degenerate, type Issue } from '../../builder/validate'
import { IssueList, StringList } from './Bits'

const TYPES: Array<Question['type']> = ['noul', 'choice', 'score']

function OptionsEditor({ q, onChange }: { q: Question; onChange: (q: Question) => void }) {
  const options = q.options || {}
  const rows = Object.entries(options)
  const bad = degenerate(options)
  const setOpt = (k: string, v: string) => onChange({ ...q, options: { ...options, [k]: v } })
  const rekey = (from: string, to: string) => {
    if (!to || (to !== from && options[to] !== undefined)) return
    onChange({ ...q, options: renameKey(options, from, to) })
  }
  const drop = (k: string) => {
    const next = { ...options }; delete next[k]
    onChange({ ...q, options: next })
  }
  const add = () => {
    let n = rows.length + 1
    while (options[`option_${n}`] !== undefined) n++
    setOpt(`option_${n}`, '')
  }
  return (
    <>
      <div className={`teach${bad ? ' bad' : ''}`}>
        <b>Describe every option by its consequence, not its name.</b>
        <p>
          The classifier never sees your code, your intent or this UI — it sees the option id and the text
          you write beside it, and nothing else. An option described only by its id (<span className="mono">backend: "backend"</span>)
          carries no information to choose on, so the answer ranks <b>at chance</b> and the gate you hang off it
          is a coin flip wearing a number.
        </p>
        <p>
          Write what <b>picking it would mean</b>: <span className="mono">backend: "the failure is in the Go API or the database, not in anything the browser runs"</span>.
          The backend refuses to load a workflow whose options are all blank, all identical, or each equal to its own id.
        </p>
      </div>
      {rows.length === 0 && <div className="muted">No options — a choice question needs at least one.</div>}
      {rows.map(([k, v]) => (
        <div className="optrow" key={k}>
          <input className="mono" value={k} onChange={e => rekey(k, e.target.value)} placeholder="option_id" />
          <textarea value={v} placeholder="what picking this option would mean" onChange={e => setOpt(k, e.target.value)} style={{ minHeight: 46 }} />
          <button type="button" className="ghost small" onClick={() => drop(k)}>×</button>
        </div>))}
      <button type="button" className="ghost small" onClick={add}>+ option</button>
      {bad && (
        <div className="issue error"><span className="mark">✕</span>
          {degeneracyReason(options)} — the loader rejects this workflow until each option says what picking it would mean.
        </div>)}
      {rows.length > 255 && <div className="issue error"><span className="mark">✕</span>a choice question takes at most 255 options.</div>}
    </>
  )
}

function QuestionCard({ qkey, q, onKey, onChange, onRemove }: {
  qkey: string; q: Question; onKey: (k: string) => void; onChange: (q: Question) => void; onRemove: () => void
}) {
  return (
    <div className="qcard">
      <div className="qhead">
        <input className="mono" value={qkey} onChange={e => onKey(e.target.value)} placeholder="question_key" />
        <select value={q.type} onChange={e => onChange({ ...emptyQuestion(e.target.value), instructions: q.instructions })}>
          {TYPES.map(t => <option key={t} value={t}>{t}</option>)}
        </select>
        <button type="button" className="ghost small" onClick={onRemove}>remove</button>
      </div>
      <label>Instructions — the question, in full</label>
      <textarea style={{ minHeight: 56 }} value={q.instructions}
        placeholder="Rate how likely an automated coding agent is to fix this report unaided, judging only the report's own content."
        onChange={e => onChange({ ...q, instructions: e.target.value })} />

      {q.type === 'noul' && (
        <div className="noulgrid">
          <div>
            <label>what <span className="mono">true</span> means</label>
            <textarea style={{ minHeight: 46 }} value={q.true || ''} onChange={e => onChange({ ...q, true: e.target.value })}
              placeholder="the report describes data exposure, privilege escalation, injection or authentication bypass" />
          </div>
          <div>
            <label>what <span className="mono">false</span> means</label>
            <textarea style={{ minHeight: 46 }} value={q.false || ''} onChange={e => onChange({ ...q, false: e.target.value })}
              placeholder="the report describes ordinary incorrect behaviour with no security consequence" />
          </div>
        </div>)}

      {q.type === 'choice' && <OptionsEditor q={q} onChange={onChange} />}

      {q.type === 'score' && (
        <>
          <label>Ordered rubric — 2 to 10 levels, worst first. Level 0 is the first row.</label>
          <div className="hint">Each level must be distinguishable from its neighbours in words, the same way options must: “possible: the wrong behaviour is clear, but neither the trigger nor the expected result is stated”.</div>
          <StringList multiline values={q.levels || []} addLabel="level" placeholder="what this level looks like"
            onChange={levels => onChange({ ...q, levels })} />
          {(q.levels?.length ?? 0) < 2 && <div className="issue error"><span className="mark">✕</span>a score needs at least 2 levels.</div>}
          {(q.levels?.length ?? 0) > 10 && <div className="issue error"><span className="mark">✕</span>a score takes at most 10 levels.</div>}
        </>)}
    </div>
  )
}

export default function DecideEditor({ decide, stepIds, onChange, issues }: {
  decide: Decide; stepIds: string[]; onChange: (d: Decide) => void; issues: Issue[]
}) {
  const qkeys = Object.keys(decide.questions || {})
  const setQuestion = (k: string, q: Question) => onChange({ ...decide, questions: { ...decide.questions, [k]: q } })
  const addQuestion = () => {
    let n = qkeys.length + 1
    while (decide.questions?.[`question_${n}`]) n++
    setQuestion(`question_${n}`, emptyQuestion('noul'))
  }
  const gates = decide.gates || []

  return (
    <div className="decideed">
      <div className="hint">
        The judge runs <b>before</b> the agent, on a small model, so a workflow can route or halt for a
        fraction of a cent instead of a full agent run. Its answers are advisory — they steer, they never authorise.
      </div>
      <label>State — the text the classifier judges (a Go template, like a prompt)</label>
      <textarea value={decide.state} onChange={e => onChange({ ...decide, state: e.target.value })}
        placeholder={'Bug title: {{ .Input.title }}\n\n{{ .Input.description }}'} />

      <div className="subhead">
        <h3>Questions</h3>
        <button type="button" className="ghost small" onClick={addQuestion}>+ question</button>
      </div>
      {qkeys.length === 0 && <div className="muted">A decide block needs at least one question.</div>}
      {qkeys.map(k => (
        <QuestionCard key={k} qkey={k} q={decide.questions[k]}
          onKey={to => {
            if (!to || (to !== k && decide.questions[to])) return
            onChange({
              ...decide,
              questions: renameKey(decide.questions, k, to),
              gates: gates.map(g => (g.question === k ? { ...g, question: to } : g)),
            })
          }}
          onChange={q => setQuestion(k, q)}
          onRemove={() => {
            const next = { ...decide.questions }; delete next[k]
            onChange({ ...decide, questions: next, gates: gates.filter(g => g.question !== k) })
          }} />))}

      <div className="subhead">
        <h3>Decide gates</h3>
        <button type="button" className="ghost small" disabled={!qkeys.length}
          onClick={() => onChange({ ...decide, gates: [...gates, emptyDecideGate(qkeys[0])] })}>+ gate</button>
      </div>
      <div className="hint">Exactly one of <span className="mono">below</span>, <span className="mono">at_least</span> or <span className="mono">is</span> per gate. Read <span className="mono">calibrated</span> before trusting a threshold — one tuned on a different backend does not transfer.</div>
      {gates.map((g, i) => {
        const q = decide.questions?.[g.question]
        const mode = g.below !== undefined ? 'below' : g.atLeast !== undefined ? 'at_least' : 'is'
        const setMode = (m: string) => {
          const base = { ...g, below: undefined, atLeast: undefined, is: undefined }
          onChange({ ...decide, gates: replaceAt(gates, i, m === 'below' ? { ...base, below: 0.5 } : m === 'at_least' ? { ...base, atLeast: 0.6 } : { ...base, is: Object.keys(q?.options || {})[0] || '' }) })
        }
        const set = (patch: Partial<typeof g>) => onChange({ ...decide, gates: replaceAt(gates, i, { ...g, ...patch }) })
        return (
          <div className="gaterow" key={i}>
            <select value={g.question} onChange={e => set({ question: e.target.value })}>
              {!qkeys.includes(g.question) && <option value={g.question}>{g.question || '— none —'}</option>}
              {qkeys.map(k => <option key={k}>{k}</option>)}
            </select>
            <select value={mode} onChange={e => setMode(e.target.value)}>
              <option value="below">below</option><option value="at_least">at_least</option><option value="is">is</option>
            </select>
            {mode === 'is'
              ? (q?.type === 'choice'
                ? <select value={g.is || ''} onChange={e => set({ is: e.target.value })}>
                    <option value="">—</option>
                    {Object.keys(q.options || {}).map(o => <option key={o}>{o}</option>)}
                  </select>
                : <input value={g.is || ''} onChange={e => set({ is: e.target.value })} placeholder="option id" />)
              : <input type="number" step="0.05" value={mode === 'below' ? (g.below ?? 0) : (g.atLeast ?? 0)}
                  onChange={e => set(mode === 'below' ? { below: Number(e.target.value) } : { atLeast: Number(e.target.value) })} />}
            <select value={g.action} onChange={e => set({ action: e.target.value })}>
              <option value="needs_input">needs_input</option><option value="fail">fail</option><option value="skip_to">skip_to</option>
            </select>
            {g.action === 'skip_to' && (
              <select value={g.skipTo || ''} onChange={e => set({ skipTo: e.target.value })}>
                <option value="">— step —</option>
                {stepIds.map(s => <option key={s}>{s}</option>)}
              </select>)}
            <textarea style={{ minHeight: 40, gridColumn: '1 / -2' }} value={g.message || ''} placeholder="message shown to the human when this gate fires"
              onChange={e => set({ message: e.target.value })} />
            <button type="button" className="ghost small" onClick={() => onChange({ ...decide, gates: removeAt(gates, i) })}>×</button>
          </div>)
      })}
      <IssueList issues={issues} />
    </div>
  )
}
