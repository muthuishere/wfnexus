import type { Budget, BuiltinTool, Doctor, Skill, Step, TeamMember } from '../../api'
import {
  emptyGate, emptyGuardrail, emptyQuestion, emptyTeamMember,
  removeAt, replaceAt,
} from '../../builder/model'
import { issuesFor, type Issue } from '../../builder/validate'
import { Field, IssueList, Picker, Section, StringList, skillOptions } from './Bits'
import DecideEditor from './DecideEditor'
import SchemaEditor from './SchemaEditor'

const BUDGET_FIELDS: Array<[keyof Budget, string, string]> = [
  ['maxTurns', 'max_turns', 'LLM turns'],
  ['maxTokens', 'max_tokens', 'tokens, whole subtree'],
  ['maxToolCalls', 'max_tool_calls', 'tool calls'],
  ['maxWallSec', 'max_wall_sec', 'wall clock, seconds'],
  ['maxChildren', 'max_children', 'sub-agents spawned'],
  ['maxConcurrent', 'max_concurrent', 'sub-agents at once'],
  ['maxDepth', 'max_depth', 'delegation depth'],
]

function BudgetGrid({ budget, onChange }: { budget?: Budget; onChange: (b: Budget) => void }) {
  const b = budget || {}
  return (
    <div className="budgetgrid">
      {BUDGET_FIELDS.map(([k, yamlName, what]) => (
        <label key={k}>
          <span className="mono">{yamlName}</span>
          <input type="number" min={0} value={b[k] ?? ''} placeholder="—"
            onChange={e => onChange({ ...b, [k]: e.target.value === '' ? undefined : Number(e.target.value) })} />
          <i className="muted">{what}</i>
        </label>))}
    </div>
  )
}

/** ModelPicker offers what this machine ACTUALLY has, not a typed-in string.
 *
 *  A provider is a name in a registry (ADR 0011) and the engine used to accept
 *  a name and then ignore it — a step saying `provider: devin` ran on the
 *  default model and said nothing. Now the name selects, so offering a name
 *  that does not resolve just moves the failure to run time. Anything the
 *  doctor marks not-ready is shown as such rather than hidden: it is a real
 *  entry, and "install this CLI" is a more useful answer than an empty list. */
function ModelPicker({ step, doctor, onChange }: {
  step: Step; doctor?: Doctor; onChange: (patch: Partial<Step>) => void
}) {
  const providers = doctor?.providers || []
  const chosen = providers.find(p => p.name === step.provider)
  const def = doctor?.default
  return (
    <div className="grid2">
      <Field label="Provider — where this step's turns come from"
        hint={chosen
          ? (chosen.ready
            ? `${chosen.kind}${chosen.detail ? ` · ${chosen.detail}` : ''}`
            : `NOT READY on this machine: ${chosen.problem}. The step will be refused rather than run on a different model.`)
          : def
            ? `The process default: ${def.model}${def.keySet ? '' : ` — but ${def.apiKeyEnv} is NOT SET`}`
            : 'The process default.'}>
        <select value={step.provider || ''} onChange={e => onChange({ provider: e.target.value || undefined })}>
          <option value="">— default ({def?.model || 'configured model'}) —</option>
          {providers.map(p => (
            <option key={p.name} value={p.name}>
              {p.ready ? '' : '⚠ '}{p.name} · {p.kind}{p.model ? ` · ${p.model}` : ''}
            </option>))}
        </select>
      </Field>
      <Field label="Model override"
        hint={step.provider
          ? 'Selects a model WITHIN that provider; it does not reach past it to the default endpoint.'
          : 'A model id on the default endpoint. Leave empty to use the configured default.'}>
        <input className="mono" list="doctor-models" value={step.model || ''}
          placeholder={chosen?.model || def?.model || ''}
          onChange={e => onChange({ model: e.target.value || undefined })} />
        <datalist id="doctor-models">{(doctor?.models || []).map(m => <option key={m} value={m} />)}</datalist>
      </Field>
    </div>
  )
}

function TeamEditor({ step, skills, tools, onChange }: {
  step: Step; skills: Skill[]; tools: BuiltinTool[]; onChange: (t: TeamMember[]) => void
}) {
  const team = step.team || []
  const set = (i: number, m: TeamMember) => onChange(replaceAt(team, i, m))
  return (
    <>
      <div className="hint">
        A team gives this step the built-in <span className="mono">task</span> tool — it does <b>not</b> make it delegate.
        Measured on a live run: a parent holding <span className="mono">bash</span> called <span className="mono">task</span> zero times, because it can
        grep and cat for itself. Delegation is only structurally enforced when the parent <b>lacks</b> the capability the
        sub-agent holds.
      </div>
      {team.map((m, i) => (
        <div className="member-ed" key={i}>
          <div className="qhead">
            <input className="mono" value={m.id} placeholder="explorer" onChange={e => set(i, { ...m, id: e.target.value })} />
            <button type="button" className="ghost small" onClick={() => onChange(removeAt(team, i))}>remove</button>
          </div>
          <Field label="does — the routing description the delegating model reads">
            <textarea style={{ minHeight: 46 }} value={m.does} placeholder="Read-only research inside the repository: finds where a symptom's code lives, reports the call path with file:line, and never edits."
              onChange={e => set(i, { ...m, does: e.target.value })} />
          </Field>
          <Field label="soul — this sub-agent's identity">
            <textarea style={{ minHeight: 46 }} value={m.soul || ''} onChange={e => set(i, { ...m, soul: e.target.value })} />
          </Field>
          <div className="two">
            <Picker title="skills" options={skillOptions(skills)} selected={m.skills || []} onChange={v => set(i, { ...m, skills: v })} />
            <Picker title="tools" options={tools.map(t => ({ name: t.name, description: t.description }))} selected={m.tools || []} onChange={v => set(i, { ...m, tools: v })} />
          </div>
          <Field label="model (optional — blank inherits the step's)">
            <input value={m.model || ''} onChange={e => set(i, { ...m, model: e.target.value })} placeholder="anthropic/claude-sonnet-4.5" />
          </Field>
          <label>budget</label>
          <BudgetGrid budget={m.budget} onChange={b => set(i, { ...m, budget: b })} />
        </div>))}
      <button type="button" className="ghost small" onClick={() => onChange([...team, emptyTeamMember(team.length + 1)])}>+ sub-agent</button>
    </>
  )
}

export default function StepEditor({ step, index, stepIds, skills, tools, doctor, issues, onChange, onRemove, onMove }: {
  step: Step
  index: number
  stepIds: string[]
  skills: Skill[]
  tools: BuiltinTool[]
  /** what this machine actually has — drives the provider and model pickers */
  doctor?: Doctor
  issues: Issue[]
  onChange: (s: Step) => void
  onRemove: () => void
  onMove: (delta: number) => void
}) {
  const set = (patch: Partial<Step>) => onChange({ ...step, ...patch })
  const mine = (prefix: string) => issuesFor(issues, index, prefix)
  const hasErr = (prefix: string) => mine(prefix).some(i => i.severity === 'error')
  const gates = step.gates || []
  const guardrails = step.guardrails || []

  return (
    <div className={`stepcard${issues.some(i => i.severity === 'error') ? ' bad' : ''}`}>
      <div className="stephead">
        <span className="num mono">{index + 1}</span>
        <input className="mono stepid" value={step.id} placeholder="step-id" onChange={e => set({ id: e.target.value })} />
        <input value={step.name || ''} placeholder="Human-readable name" onChange={e => set({ name: e.target.value })} />
        <div className="stepacts">
          <button type="button" className="ghost small" onClick={() => onMove(-1)} disabled={index === 0}>↑</button>
          <button type="button" className="ghost small" onClick={() => onMove(1)} disabled={index === stepIds.length - 1}>↓</button>
          <button type="button" className="ghost small danger-text" onClick={onRemove}>delete</button>
        </div>
      </div>
      <IssueList issues={mine('id')} />

      <Field label="Description — what this step is for">
        <input value={step.description || ''} onChange={e => set({ description: e.target.value })} />
      </Field>

      <ModelPicker step={step} doctor={doctor} onChange={set} />

      <Field label="Soul — the agent's identity, its system prompt"
        hint="Written in the second person and about character, not task: “You are an adversarial reviewer who did not write this code and does not trust it.”">
        <textarea value={step.soul || ''} onChange={e => set({ soul: e.target.value })} />
      </Field>

      <Field label="Prompt — the task, as a Go template" issues={mine('prompt')}
        hint={<>Prior steps hand this one typed objects: <span className="mono">{'{{ .Steps.<step-id>.<field> }}'}</span>, <span className="mono">{'{{ .Input.<field> }}'}</span>, <span className="mono">{'{{ .Decide.<question> }}'}</span>. Helpers: <span className="mono">json</span>, <span className="mono">join</span>, <span className="mono">list</span>, <span className="mono">default</span>.</>}>
        <textarea style={{ minHeight: 160 }} value={step.prompt || ''} onChange={e => set({ prompt: e.target.value })} />
      </Field>

      <div className="two">
        <div>
          <Picker title="skills" options={skillOptions(skills)} selected={step.skills || []} onChange={v => set({ skills: v })}
            emptyNote="skill registry unavailable — names cannot be checked" />
          <IssueList issues={mine('skills')} />
        </div>
        <div>
          <Picker title="built-in tools" options={tools.map(t => ({ name: t.name, description: t.description }))}
            selected={step.tools || []} onChange={v => set({ tools: v })} emptyNote="tool catalog unavailable" />
          <IssueList issues={mine('tools')} />
          <div className="hint">This list is the whole security model: a tool you leave out cannot be called at all.</div>
        </div>
      </div>

      <Section title="Output contract" defaultOpen tone={hasErr('outputSchema') ? 'err' : undefined}>
        <SchemaEditor label="output_schema" schema={step.outputSchema} onChange={s => set({ outputSchema: s })}
          hint="This becomes the submit_output tool. Validation happens inside the agent loop, so a rejected submission comes back as a tool result and the model corrects itself instead of the step dying." />
        <IssueList issues={mine('outputSchema')} />
      </Section>

      <Section title="Budget & limits">
        <BudgetGrid budget={step.budget} onChange={b => set({ budget: b })} />
        <div className="three" style={{ marginTop: 10 }}>
          <Field label="model"><input value={step.model || ''} placeholder="inherit" onChange={e => set({ model: e.target.value })} /></Field>
          <Field label="max_turns (step)"><input type="number" min={0} value={step.maxTurns ?? ''} placeholder="30" onChange={e => set({ maxTurns: e.target.value === '' ? undefined : Number(e.target.value) })} /></Field>
          <Field label="max_attempts"><input type="number" min={0} value={step.maxAttempts ?? ''} placeholder="3" onChange={e => set({ maxAttempts: e.target.value === '' ? undefined : Number(e.target.value) })} /></Field>
          <Field label="timeout_sec"><input type="number" min={0} value={step.timeoutSec ?? ''} placeholder="—" onChange={e => set({ timeoutSec: e.target.value === '' ? undefined : Number(e.target.value) })} /></Field>
        </div>
        <div className="togs">
          <label className="inline"><input type="checkbox" checked={!!step.requiresApproval} onChange={e => set({ requiresApproval: e.target.checked })} /> requires_approval — halt <i>before</i> this step; the model is never called until a human approves</label>
          <label className="inline"><input type="checkbox" checked={!!step.askHuman} onChange={e => set({ askHuman: e.target.checked })} /> ask_human — grant the <span className="mono">question</span> built-in; the run parks in needs_input until answered</label>
        </div>
        <Field label="mcp servers" hint="Names from mcp.json. Empty means this step sees none.">
          <StringList values={step.mcp || []} addLabel="server" placeholder="server-name" onChange={v => set({ mcp: v })} />
        </Field>
      </Section>

      <Section title="Guardrails" count={guardrails.length} tone={hasErr('guardrails') ? 'err' : undefined}
        hint={<>Policy on a tool call — “may it?”, never “is it right?”. First deny wins, and the model is shown your <b>reason</b> as the tool result, so the reason is the only thing that tells it what to do instead. The backend refuses a guardrail with no reason.</>}>
        {guardrails.map((g, i) => (
          <div className="guard-ed" key={i}>
            <div className="grow">
              <Field label="deny — tool name, or * for any"><input className="mono" value={g.deny} onChange={e => set({ guardrails: replaceAt(guardrails, i, { ...g, deny: e.target.value }) })} /></Field>
              <button type="button" className="ghost small" onClick={() => set({ guardrails: removeAt(guardrails, i) })}>×</button>
            </div>
            <Field label="args_contain — deny only when an argument contains one of these (empty ⇒ deny outright)">
              <StringList values={g.argsContain || []} addLabel="fragment" placeholder="git push"
                onChange={v => set({ guardrails: replaceAt(guardrails, i, { ...g, argsContain: v }) })} />
            </Field>
            <Field label="reason — shown to the model verbatim" issues={mine(`guardrails.${i}.reason`)}>
              <textarea style={{ minHeight: 46 }} value={g.reason} placeholder="publishing is owner-gated in this workflow — commit locally and let the approval gate decide"
                onChange={e => set({ guardrails: replaceAt(guardrails, i, { ...g, reason: e.target.value }) })} />
            </Field>
          </div>))}
        <button type="button" className="ghost small" onClick={() => set({ guardrails: [...guardrails, emptyGuardrail()] })}>+ guardrail</button>
      </Section>

      <Section title="Team — sub-agents" count={(step.team || []).length} tone={hasErr('team') ? 'err' : undefined}>
        <TeamEditor step={step} skills={skills} tools={tools} onChange={t => set({ team: t })} />
        <IssueList issues={mine('team')} />
      </Section>

      <Section title="Decide — the judge tier" count={step.decide ? Object.keys(step.decide.questions || {}).length : 0}
        tone={hasErr('decide') ? 'err' : undefined}>
        {!step.decide
          ? <button type="button" className="ghost small"
              onClick={() => set({ decide: { state: '', questions: { actionable: emptyQuestion('noul') }, gates: [] } })}>
              + add a decide block
            </button>
          : <>
              <DecideEditor decide={step.decide} stepIds={stepIds} issues={mine('decide')} onChange={d => set({ decide: d })} />
              <button type="button" className="ghost small" style={{ marginTop: 8 }} onClick={() => set({ decide: undefined })}>remove decide block</button>
            </>}
      </Section>

      <Section title="Gates — what the workflow does with the output" count={gates.length} tone={hasErr('gates') ? 'err' : undefined}
        hint={<>A gate fires when a top-level output field <span className="mono">equals</span> a value. <span className="mono">needs_input</span> pauses for a human, <span className="mono">fail</span> stops the run, <span className="mono">skip_to</span> jumps to another step — which must exist.</>}>
        {gates.map((g, i) => {
          const setG = (patch: Partial<typeof g>) => set({ gates: replaceAt(gates, i, { ...g, ...patch }) })
          const props = Object.keys(step.outputSchema?.properties || {})
          return (
            <div className="gaterow wide" key={i}>
              <select value={g.field} onChange={e => setG({ field: e.target.value })}>
                <option value="">— field —</option>
                {!props.includes(g.field) && g.field && <option value={g.field}>{g.field}</option>}
                {props.map(p => <option key={p}>{p}</option>)}
              </select>
              <select value={JSON.stringify(g.equals)} onChange={e => setG({ equals: JSON.parse(e.target.value) as unknown })}>
                <option value="false">equals false</option>
                <option value="true">equals true</option>
                <option value='""'>equals "" (custom)</option>
              </select>
              {typeof g.equals === 'string' && <input value={g.equals} onChange={e => setG({ equals: e.target.value })} placeholder="value" />}
              <select value={g.action} onChange={e => setG({ action: e.target.value })}>
                <option value="needs_input">needs_input</option><option value="fail">fail</option><option value="skip_to">skip_to</option>
              </select>
              {g.action === 'skip_to' && (
                <select value={g.skipTo || ''} onChange={e => setG({ skipTo: e.target.value })}>
                  <option value="">— step —</option>
                  {stepIds.filter(s => s !== step.id).map(s => <option key={s}>{s}</option>)}
                </select>)}
              <textarea style={{ minHeight: 40, gridColumn: '1 / -2' }} value={g.message || ''}
                placeholder={'Need more information: {{ join .Output.missing_info " | " }}'}
                onChange={e => setG({ message: e.target.value })} />
              <button type="button" className="ghost small" onClick={() => set({ gates: removeAt(gates, i) })}>×</button>
            </div>)
        })}
        <button type="button" className="ghost small" onClick={() => set({ gates: [...gates, emptyGate()] })}>+ gate</button>
        <IssueList issues={mine('gates')} />
      </Section>
    </div>
  )
}
