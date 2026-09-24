import { useEffect, useMemo, useRef, useState } from 'react'
import { api, type BuiltinTool, type Doctor, type Skill, type WorkflowDraft } from '../api'
import { blankStep, forSave, moveItem, removeAt, replaceAt, templateDraft } from '../builder/model'
import { errorsOnly, validateDraft, type Issue } from '../builder/validate'
import { toYaml } from '../builder/yaml'
import type { JsonSlots } from '../builder/jsonschema'
import JsonSchemaEditor from '../components/builder/JsonSchemaEditor'
import StepEditor, { EnvEditor } from '../components/builder/StepEditor'
import WorkflowCanvas from '../components/WorkflowCanvas'
import FileList from '../components/FileList'
import { Field, IssueList } from '../components/builder/Bits'

/** The builder edits ONE thing at a time.
 *
 *  The diagram on top is the navigation — it is the only view of a workflow
 *  whose order is derived rather than authored, so it is the thing an author
 *  already looks at, and clicking a node edits that step. Below it, a list
 *  that selects, and one pane that shows only what was selected. The page it
 *  replaces stacked every step's full editor down one scroll, which is why a
 *  twelve-control output-contract row could not be attributed to anything. */
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
  /** -1 is the workflow itself; otherwise the index of the step being edited. */
  const [sel, setSel] = useState(0)

  // Raw schema text and its parse error, per box, held here so a half-typed
  // contract survives selecting another step. See JsonSchemaEditor.
  const [jsonText, setJsonText] = useState<Record<string, string>>({})
  const [jsonErrs, setJsonErrs] = useState<Record<string, string>>({})
  const slots: JsonSlots = {
    text: jsonText,
    errors: jsonErrs,
    setText: (k, v) => setJsonText(t => ({ ...t, [k]: v })),
    setError: (k, m) => setJsonErrs(e => (e[k] === m ? e : { ...e, [k]: m })),
  }

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
      .then(w => { if (live) setDraft({ name: w.name, description: w.description, inputSchema: w.inputSchema || { type: 'object' }, steps: w.steps || [], env: w.env, mount: w.mount, files: w.files }) })
      .catch(e => live && setLoadErr(e instanceof Error ? e.message : String(e)))
    return () => { live = false }
  }, [name])

  // Once the registry lands, seed the blank template's first skill — the point
  // of the template is that it is already a workflow that would load.
  useEffect(() => {
    if (editing || !skills.length) return
    setDraft(d => (d && d.steps.length === 1 && !d.steps[0].skills?.length && !d.name
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

  // The server is the authority: it runs the loader, the catalog and the
  // JSON-schema compiler. Asked on a debounce so typing is not a request
  // storm, and only reported — the save button already shows what it refused.
  const [verdict, setVerdict] = useState('')
  const seq = useRef(0)
  useEffect(() => {
    if (!draft?.name) { setVerdict(''); return }
    const mine = ++seq.current
    const t = setTimeout(() => {
      api.validateWorkflow(forSave(draft))
        .then(v => { if (mine === seq.current) setVerdict(v.valid ? '' : v.error || 'refused, with no reason given') })
        .catch(e => { if (mine === seq.current) setVerdict(e instanceof Error ? e.message : String(e)) })
    }, 500)
    return () => clearTimeout(t)
  }, [draft])

  if (loadErr) return <div className="banner err">Could not load <span className="mono">{name}</span> — {loadErr}</div>
  if (!draft) return <div className="muted">loading…</div>

  const set = (patch: Partial<WorkflowDraft>) => { setDraft({ ...draft, ...patch }); setSaved('') }
  const stepIds = draft.steps.map(s => s.id)
  const at = Math.min(sel, draft.steps.length - 1)
  const step = at >= 0 ? draft.steps[at] : undefined

  // A schema box that does not parse blocks the save outright: the committed
  // schema is still the last valid one, so saving would quietly write
  // something other than what is on screen.
  const badJson = Object.entries(jsonErrs)
    .filter(([slot, msg]) => msg && (slot === 'input' || Number(slot.split(':')[1]) < draft.steps.length))
  const blocked = errs.length + badJson.length

  const pick = (id: string) => {
    const i = draft.steps.findIndex(s => s.id === id)
    if (i >= 0) setSel(i)
  }

  const save = async () => {
    setBusy(true); setSaveErr(''); setSaved('')
    try {
      const body = forSave(draft)
      await api.saveWorkflow(body.name, body)
      setSaved(`Saved ${body.name}.`)
      if (!editing) location.hash = `#/workflows/${body.name}/edit`
    } catch (e) {
      setSaveErr(e instanceof Error ? e.message : String(e))
    } finally { setBusy(false) }
  }

  const slotLabel = (slot: string) =>
    slot === 'input' ? 'input_schema' : `${draft.steps[Number(slot.split(':')[1])]?.id || slot} · output_schema`

  return (
    <>
      <div className="buildhead">
        <div>
          <h1>{editing ? `Edit · ${name}` : 'New workflow'}</h1>
        </div>
        <div className="actions" style={{ marginLeft: 'auto', marginTop: 0 }}>
          <button className="ghost" onClick={() => setShowYaml(!showYaml)}>{showYaml ? 'Hide' : 'Show'} YAML</button>
          <a href="#/workflows"><button className="ghost">Back</button></a>
          <button disabled={busy || blocked > 0} onClick={save}>
            {busy ? 'saving…' : blocked ? `${blocked} problem${blocked > 1 ? 's' : ''} to fix` : editing ? 'Save workflow' : 'Create workflow'}
          </button>
        </div>
      </div>

      {catalogErr && <div className="banner warn">Skill/tool registries unavailable — {catalogErr}.</div>}
      {saveErr && <div className="banner err"><b>The API refused this workflow.</b><div className="mono" style={{ marginTop: 6 }}>{saveErr}</div></div>}
      {saved && <div className="banner ok">{saved}</div>}
      {badJson.length > 0 && (
        <div className="banner err">
          <div className="errlist">
            {badJson.map(([slot, msg]) => (
              <div key={slot}>
                <button type="button" className="errjump mono"
                  onClick={() => setSel(slot === 'input' ? -1 : Number(slot.split(':')[1]))}>{slotLabel(slot)}</button>
                {' '}{msg}
              </div>))}
          </div>
        </div>)}
      {errs.length > 0 && (
        <div className="banner err">
          <div className="errlist">
            {errs.map((e, i) => (
              <div key={i}>
                <button type="button" className="errjump mono" onClick={() => setSel(e.step)}>
                  {e.step < 0 ? 'workflow' : `${draft.steps[e.step]?.id || `step ${e.step + 1}`}.${e.field}`}
                </button> {e.message}
              </div>))}
          </div>
        </div>)}
      {verdict && <div className="banner err"><b>The loader would refuse this.</b><div className="mono" style={{ marginTop: 6 }}>{verdict}</div></div>}

      <div className="card canvascard">
        <WorkflowCanvas steps={draft.steps} active={step?.id} onPick={pick} />
      </div>

      <div className={`buildbody${showYaml ? ' withyaml' : ''}`}>
        <nav className="stepnav" aria-label="steps">
          <button type="button" className={`snav${at < 0 ? ' on' : ''}${issues.some(i => i.step === -1 && i.severity === 'error') ? ' bad' : ''}`}
            onClick={() => setSel(-1)}>
            <span className="t">{draft.name || 'untitled'}</span>
            <span className="k">workflow</span>
          </button>
          {draft.steps.map((s, i) => {
            const n = issues.filter(x => x.step === i && x.severity === 'error').length + (jsonErrs[`step:${i}`] ? 1 : 0)
            return (
              <button type="button" key={i} className={`snav${i === at ? ' on' : ''}${n ? ' bad' : ''}`} onClick={() => setSel(i)}>
                <span className="n mono">{i + 1}</span>
                <span className="t">{s.id || 'untitled'}</span>
                {n > 0 && <span className="badge failed">{n}</span>}
              </button>)
          })}
          <button type="button" className="ghost small addstep"
            onClick={() => { set({ steps: [...draft.steps, blankStep(draft.steps.length + 1)] }); setSel(draft.steps.length) }}>
            + step
          </button>
        </nav>

        <div className="buildmain">
          {at < 0 ? (
            <div className="card">
              <Field label="Workflow name" issues={issues.filter(i => i.step === -1 && i.field === 'name')}>
                <input className="mono" value={draft.name} placeholder="bug-fix" disabled={editing}
                  onChange={e => set({ name: e.target.value })} />
              </Field>
              <Field label="Description">
                <textarea style={{ minHeight: 60 }} value={draft.description} onChange={e => set({ description: e.target.value })} />
              </Field>
              <Field label="input_schema — the form that starts a run">
                <JsonSchemaEditor slot="input" schema={draft.inputSchema} slots={slots}
                  onChange={s => set({ inputSchema: s })} />
              </Field>
              <Field label="env — every step gets these">
                <EnvEditor env={draft.env || {}} onChange={env => set({ env: Object.keys(env).length ? env : undefined })} />
              </Field>
              <Field label="mount — folders this workflow needs">
                <MountEditor mount={draft.mount || []} onChange={mount => set({ mount: mount.length ? mount : undefined })} />
              </Field>
              {!!draft.files?.length && (
                <Field label="files shipped beside this workflow">
                  <FileList files={draft.files} />
                </Field>)}
              <IssueList issues={issues.filter(i => i.step === -1 && i.field === 'steps')} />
            </div>
          ) : step ? (
            <StepEditor key={at} step={step} index={at} stepIds={stepIds} skills={skills} tools={tools} doctor={doctor}
              slots={slots}
              issues={issues.filter(x => x.step === at)}
              onChange={next => set({ steps: replaceAt(draft.steps, at, next) })}
              onRemove={() => { set({ steps: removeAt(draft.steps, at) }); setSel(Math.max(0, at - 1)) }}
              onMove={d => { set({ steps: moveItem(draft.steps, at, d) }); setSel(Math.min(Math.max(at + d, 0), draft.steps.length - 1)) }} />
          ) : <div className="muted">No steps yet.</div>}
        </div>

        {showYaml && (
          <div className="yamlpane">
            <div className="card">
              <div className="subhead">
                <h3 style={{ margin: 0 }}>YAML</h3>
                <button className="ghost small" onClick={() => {
                  navigator.clipboard?.writeText(yaml).then(() => { setCopied(true); setTimeout(() => setCopied(false), 1500) }, () => setCopied(false))
                }}>{copied ? 'copied' : 'copy'}</button>
              </div>
              <pre>{yaml}</pre>
            </div>
          </div>)}
      </div>
    </>
  )
}

/** Attached folders, one per line, exactly as the file spells them.
 *
 *  A textarea rather than a row-per-field table on purpose: the file's form IS
 *  one line, and a three-column editor would teach a shape the YAML does not
 *  have. What the box holds is what the file holds. */
function MountEditor({ mount, onChange }: { mount: string[]; onChange: (m: string[]) => void }) {
  return (
    <textarea className="mono" style={{ minHeight: 70, width: '100%' }}
      value={mount.join('\n')}
      placeholder={'/Users/me/datasets:data:ro\nreports:out:rw'}
      onChange={e => onChange(e.target.value.split('\n').map(l => l.trim()).filter(Boolean))} />
  )
}
