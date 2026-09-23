import type { Budget, Step } from '../api'
import OutputContract from './OutputContract'

const budgetLine = (b?: Budget) => {
  if (!b) return ''
  const parts = [
    b.maxTurns && `${b.maxTurns} turns`,
    b.maxTokens && `${b.maxTokens.toLocaleString()} tokens`,
    b.maxToolCalls && `${b.maxToolCalls} tool calls`,
    b.maxWallSec && `${b.maxWallSec}s wall`,
    b.maxChildren && `${b.maxChildren} children`,
    b.maxDepth && `depth ${b.maxDepth}`,
  ].filter(Boolean)
  return parts.join(' · ')
}

function Panel({ title, note, children }: { title: string; note?: string; children: React.ReactNode }) {
  return (
    <div className="hpanel">
      <h3>{title}{note && <span className="muted"> · {note}</span>}</h3>
      {children}
    </div>
  )
}

/** One step rendered as what it is: an entire agent with a scoped harness. */
export default function StepHarness({ step, index }: { step: Step; index: number }) {
  const b = budgetLine(step.budget) || budgetLine({ maxTurns: step.maxTurns })
  const decide = step.decide
  const team = step.team || []
  const guardrails = step.guardrails || []

  return (
    <div className="agentcard">
      <div className="agenthead">
        <div className="num">{index + 1}</div>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div className="titleline">
            <b>{step.name}</b>
            <span className="mono muted">{step.id}</span>
            {step.requiresApproval && <span className="badge awaiting_approval">approval gate</span>}
            {step.askHuman && <span className="badge needs_input">may ask human</span>}
            {decide && <span className="badge running">judge first</span>}
          </div>
          {step.description && <div className="muted">{step.description}</div>}
        </div>
        <div className="muted mono limits">{b || 'no budget set'}{step.maxAttempts ? ` · ${step.maxAttempts} submit attempts` : ''}{step.model ? ` · ${step.model}` : ''}</div>
      </div>

      {step.soul && (
        <Panel title="Soul" note="identity / system prompt">
          <div className="soul">{step.soul}</div>
        </Panel>)}

      <div className="hgrid">
        <Panel title="Skills" note={`${step.skills?.length ?? 0}`}>
          <div className="chips">{step.skills?.length ? step.skills.map(x => <span key={x}>{x}</span>) : <span className="off">none</span>}</div>
        </Panel>
        <Panel title="Tools" note={`${step.tools?.length ?? 0} of the built-ins`}>
          <div className="chips">{step.tools?.length ? step.tools.map(x => <span key={x}>{x}</span>) : <span className="off">none — no built-ins at all</span>}</div>
        </Panel>
        <Panel title="MCP" note={step.mcp?.length ? undefined : 'none granted'}>
          <div className="chips">{step.mcp?.length ? step.mcp.map(x => <span key={x}>{x}</span>) : <span className="off">none</span>}</div>
        </Panel>
      </div>

      {team.length > 0 && (
        <Panel title="Team" note={`${team.length} sub-agents, reachable via the \`task\` tool`}>
          <div className="teamlist">
            {team.map(m => (
              <div className="member" key={m.id}>
                <div><b className="mono">{m.id}</b> <span className="muted">{m.does}</span></div>
                <div className="chips">
                  {(m.skills || []).map(x => <span key={`s${x}`}>skill:{x}</span>)}
                  {(m.tools || []).map(x => <span key={`t${x}`}>{x}</span>)}
                  {m.model && <span>{m.model}</span>}
                  {budgetLine(m.budget) && <span>{budgetLine(m.budget)}</span>}
                </div>
              </div>))}
          </div>
        </Panel>)}

      {guardrails.length > 0 && (
        <Panel title="Guardrails" note="policy checked before a tool call runs">
          {guardrails.map((g, i) => (
            <div className="guard" key={i}>
              <span className="badge failed">deny</span>
              <span className="mono">{g.deny}</span>
              {g.argsContain?.length ? <span className="muted mono"> when args contain {g.argsContain.join(' | ')}</span> : null}
              <div className="muted">{g.reason}</div>
            </div>))}
        </Panel>)}

      {decide && (
        <Panel title="Decide" note={`classifier runs before the agent · state "${decide.state}"`}>
          {Object.entries(decide.questions || {}).map(([key, q]) => (
            <div className="question" key={key}>
              <div><b className="mono">{key}</b> <span className="badge pending">{q.type}</span></div>
              <div className="muted">{q.instructions}</div>
              {q.options && <div className="opts">{Object.entries(q.options).map(([id, desc]) => (
                <div key={id}><span className="mono">{id}</span> <span className="muted">— {desc}</span></div>))}</div>}
              {q.levels && <div className="chips">{q.levels.map(l => <span key={l}>{l}</span>)}</div>}
              {(q.true || q.false) && <div className="muted mono" style={{ fontSize: 11 }}>true: {q.true} · false: {q.false}</div>}
            </div>))}
          {(decide.gates || []).map((g, i) => (
            <div className="gate" key={i}>
              <span className="mono">{g.question}</span>{' '}
              {g.below !== undefined && <>below {g.below}</>}
              {g.atLeast !== undefined && <>at least {g.atLeast}</>}
              {g.is !== undefined && <>is {g.is}</>}
              {' → '}<span className={`badge ${g.action}`}>{g.action}{g.skipTo ? ` ${g.skipTo}` : ''}</span>
              {g.message && <div className="muted">{g.message}</div>}
            </div>))}
        </Panel>)}

      <Panel title="Output contract" note="must validate before the step can finish">
        <OutputContract schema={step.outputSchema} />
      </Panel>

      {(step.gates || []).length > 0 && (
        <Panel title="Gates" note="what the workflow does with that output">
          {(step.gates || []).map((g, i) => (
            <div className="gate" key={i}>
              <span className="mono">{g.field} = {JSON.stringify(g.equals)}</span>{' → '}
              <span className={`badge ${g.action}`}>{g.action}{g.skipTo ? ` ${g.skipTo}` : ''}</span>
              {g.message && <div className="muted">{g.message}</div>}
            </div>))}
        </Panel>)}
    </div>
  )
}
