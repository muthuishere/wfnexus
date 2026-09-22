import { useEffect, useMemo, useState } from 'react'
import { api, type BuiltinTool, type Doctor, type Skill, type WorkflowDraft } from '../api'
import { blankStep, forSave, moveItem, removeAt, replaceAt, templateDraft } from '../builder/model'
import { errorsOnly, validateDraft, type Issue } from '../builder/validate'
import { toYaml } from '../builder/yaml'
import SchemaEditor from '../components/builder/SchemaEditor'
import StepEditor from '../components/builder/StepEditor'
import WorkflowCanvas from '../components/WorkflowCanvas'
import { Field, IssueList, Section } from '../components/builder/Bits'

export default function WorkflowBuilderPage({ name }: { name?: string }) {
  const editing = !!name
  // The blank template is the initial state, not an effect — #/workflows/new
  // should render an editable workflow on the first paint.
  const [draft, setDraft] = useState<WorkflowDraft | undefined>(() => (name ? undefined : templateDraft(undefined, 'read')))
  const [skills, setSkills] = useState<Skill[]>([])
  const [tools, setTools] = useState<BuiltinTool[]>([])
  const [doctor, setDoctor] = useState<Doctor>()
  const [catalogErr, setCatalogErr] = useState('')
  const [loadErr, setLoadErr] = useState('')
  const [saveErr, setSaveErr] = useState('')
  const [saved, setSaved] = useState('')
  const [busy, setBusy] = useState(false)
  const [showYaml, setShowYaml] = useState(true)
  const [copied, setCopied] = useState(false)

  useEffect(() => {
    let live = true
    // The doctor drives the provider and model pickers, so the builder offers
    // what this machine actually has rather than a free-text field.
    Promise.all([api.skills(), api.tools(), api.doctor()])
      .then(([reg, t, d]) => { if (!live) return; setSkills(reg.skills); setTools(t); setDoctor(d) })
      .catch(e => live && setCatalogErr(e instanceof Error ? e.message : String(e)))
    return () => { live = false }
  }, [])

  useEffect(() => {
    if (!name) return
    let live = true
    api.workflow(name)
      .then(w => { if (live) setDraft({ name: w.name, description: w.description, inputSchema: w.inputSchema || { type: 'object' }, steps: w.steps || [] }) })
      .catch(e => live && setLoadErr(e instanceof Error ? e.message : String(e)))
    return () => { live = false }
  }, [name])

  // Once the registry lands, seed the blank template's first skill — the point
  // of the template is that it is already a workflow that would load.
  useEffect(() => {
    if (editing || !skills.length) return
    setDraft(d => (d && d.steps.length === 1 && !d.steps[0].skills.length && !d.name
      ? { ...d, steps: [{ ...d.steps[0], skills: [skills[0].name] }] }
      : d))
  }, [skills, editing])

  const known = useMemo(() => (
    skills.length || tools.length
      ? { skills: new Set(skills.map(s => s.name)), tools: new Set(tools.map(t => t.name)) }
      : undefined
  ), [skills, tools])

  const issues: Issue[] = useMemo(() => (draft ? validateDraft(draft, known) : []), [draft, known])
  const errs = errorsOnly(issues)
  const yaml = useMemo(() => (draft ? toYaml(forSave(draft)) : ''), [draft])

  if (loadErr) return <div className="banner err">Could not load <span className="mono">{name}</span> — {loadErr}</div>
  if (!draft) return <div className="muted">loading…</div>

  const set = (patch: Partial<WorkflowDraft>) => { setDraft({ ...draft, ...patch }); setSaved('') }
  const stepIds = draft.steps.map(s => s.id)

  const save = async () => {
    setBusy(true); setSaveErr(''); setSaved('')
    try {
      const body = forSave(draft)
      await api.saveWorkflow(body.name, body)
      setSaved(`Saved ${body.name}. The loader accepted it.`)
      if (!editing) location.hash = `#/workflows/${body.name}/edit`
    } catch (e) {
      setSaveErr(e instanceof Error ? e.message : String(e))
    } finally { setBusy(false) }
  }

  return (
    <>
      <div className="buildhead">
        <div>
          <h1>{editing ? `Edit · ${name}` : 'New workflow'}</h1>
          <div className="muted">A step is a whole agent — soul, scoped skills and tools, a team, a budget, a policy, and an output contract it must satisfy to finish.</div>
        </div>
        <div className="actions" style={{ marginLeft: 'auto', marginTop: 0 }}>
          <button className="ghost" onClick={() => setShowYaml(!showYaml)}>{showYaml ? 'Hide' : 'Show'} YAML</button>
          <a href="#/workflows"><button className="ghost">Back</button></a>
          <button disabled={busy || errs.length > 0} onClick={save}>
            {busy ? 'saving…' : errs.length ? `${errs.length} problem${errs.length > 1 ? 's' : ''} to fix` : editing ? 'Save workflow' : 'Create workflow'}
          </button>
        </div>
      </div>

      {catalogErr && <div className="banner warn">Skill/tool registries unavailable — {catalogErr}. Names cannot be checked against the catalog here, and the backend will reject any that do not exist.</div>}
      {saveErr && <div className="banner err"><b>The API refused this workflow.</b><div className="mono" style={{ marginTop: 6 }}>{saveErr}</div></div>}
      {saved && <div className="banner ok">{saved}</div>}
      {errs.length > 0 && (
        <div className="banner err">
          <b>{errs.length} thing{errs.length > 1 ? 's' : ''} the loader would reject.</b> The backend refuses the whole file on the first one it hits, so they are all listed here instead.
          <div className="errlist">
            {errs.map((e, i) => <div key={i}><span className="mono">{e.step < 0 ? 'workflow' : `${draft.steps[e.step]?.id || `step ${e.step + 1}`}.${e.field}`}</span> {e.message}</div>)}
          </div>
        </div>)}

      <div className={showYaml ? 'buildgrid' : ''}>
        <div>
          <div className="card">
            <Field label="Workflow name" issues={issues.filter(i => i.step === -1 && i.field === 'name')}
              hint="Lowercase and hyphenated — it becomes the filename, the URL and the id runs are created against.">
              <input className="mono" value={draft.name} placeholder="bug-fix" disabled={editing}
                onChange={e => set({ name: e.target.value })} />
            </Field>
            <Field label="Description">
              <textarea style={{ minHeight: 60 }} value={draft.description} onChange={e => set({ description: e.target.value })} />
            </Field>
            <Section title="Input schema — the form that starts a run" defaultOpen={false}
              hint="Each property becomes a field on the New run page. `format: textarea` renders a big box; `default` pre-fills it.">
              <SchemaEditor label="input_schema" schema={draft.inputSchema} onChange={s => set({ inputSchema: s })} />
            </Section>
          </div>

          <div className="card">
            <div className="subhead">
              <h3 style={{ margin: 0 }}>Shape</h3>
            </div>
            <div className="muted" style={{ fontSize: 12, marginBottom: 8 }}>
              The execution order is derived, not authored — from <span className="mono">needs:</span>,
              or from <span className="mono">consumes:</span>/<span className="mono">produces:</span>, or plain
              sequence. This is that derivation, so a mistake in it is visible before a run.
            </div>
            <WorkflowCanvas steps={draft.steps} />
          </div>

          <div className="card">
            <div className="subhead">
              <h2 style={{ margin: 0 }}>Steps <span className="muted" style={{ fontWeight: 400, fontSize: 13 }}>{draft.steps.length}</span></h2>
              <button className="ghost small" onClick={() => set({ steps: [...draft.steps, blankStep(draft.steps.length + 1)] })}>+ step</button>
            </div>
            <IssueList issues={issues.filter(i => i.step === -1 && i.field === 'steps')} />
            <div className="stepstack">
              {draft.steps.map((s, i) => (
                <StepEditor key={i} step={s} index={i} stepIds={stepIds} skills={skills} tools={tools} doctor={doctor}
                  issues={issues.filter(x => x.step === i)}
                  onChange={next => set({ steps: replaceAt(draft.steps, i, next) })}
                  onRemove={() => set({ steps: removeAt(draft.steps, i) })}
                  onMove={d => set({ steps: moveItem(draft.steps, i, d) })} />))}
            </div>
          </div>
        </div>

        {showYaml && (
          <div className="yamlpane">
            <div className="card">
              <div className="subhead">
                <h3 style={{ margin: 0 }}>YAML preview</h3>
                <button className="ghost small" onClick={() => {
                  navigator.clipboard?.writeText(yaml).then(() => { setCopied(true); setTimeout(() => setCopied(false), 1500) }, () => setCopied(false))
                }}>{copied ? 'copied' : 'copy'}</button>
              </div>
              <div className="muted" style={{ fontSize: 12, marginBottom: 8 }}>
                Exactly what a <span className="mono">workflows/*.yaml</span> file would contain. Saving PUTs the same definition as JSON.
              </div>
              <pre style={{ maxHeight: '72vh' }}>{yaml}</pre>
            </div>
          </div>)}
      </div>
    </>
  )
}
