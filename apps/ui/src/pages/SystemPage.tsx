import { useCallback, useEffect, useState } from 'react'
import {
  api, type Classifier, type Doctor, type DoctorEntry, type McpServer, type Provider, type Skipped,
} from '../api'
import { Field, Section } from '../components/builder/Bits'
import DataTable, { type Column, type Filter } from '../components/DataTable'
import EnvStore from '../components/EnvStore'

/** SystemPage answers one question: if a step named this right now, would it run?
 *
 *  Every registry entry is a NAME (ADR 0011), and a name resolves against THIS
 *  machine — an environment variable that may not be set, a CLI that may not be
 *  installed, a shell that may not exist on Windows. That used to be
 *  discoverable only by starting a run and reading the failure.
 *
 *  It never shows a secret. A provider carries the NAME of its key variable and
 *  whether it is set; the value has no path into this page. */
export default function SystemPage() {
  const [doc, setDoc] = useState<Doctor>()
  const [providers, setProviders] = useState<Provider[]>([])
  const [classifiers, setClassifiers] = useState<Classifier[]>([])
  const [mcp, setMcp] = useState<McpServer[]>([])
  const [skipped, setSkipped] = useState<Skipped[]>([])
  const [err, setErr] = useState('')
  const [note, setNote] = useState('')

  const load = useCallback(() => {
    setErr('')
    Promise.all([api.doctor(), api.providers(), api.classifiers(), api.mcp()])
      .then(([d, p, c, m]) => {
        setDoc(d)
        setProviders(p.entries || [])
        setClassifiers(c.entries || [])
        setMcp(m.entries || [])
        setSkipped([...(p.skipped || []), ...(c.skipped || []), ...(m.skipped || [])])
      })
      .catch(e => setErr(e instanceof Error ? e.message : String(e)))
  }, [])

  useEffect(load, [load])

  const act = async (what: string, fn: () => Promise<unknown>) => {
    setErr(''); setNote('')
    try { await fn(); setNote(what); load() } catch (e) { setErr(e instanceof Error ? e.message : String(e)) }
  }

  if (err && !doc) return <div className="banner err">{err}</div>
  if (!doc) return <div className="muted">checking this machine…</div>

  return (
    <>
      <div className="head">
        <div>
          <h1>System</h1>
          <p className="muted">
            What is actually wired here, as opposed to what is declared. A ✗ means a step naming
            it fails when it runs — not when it is authored.
          </p>
        </div>
        <button onClick={load}>Re-check</button>
      </div>

      {err && <div className="banner err">{err}</div>}
      {note && <div className="banner ok">{note}</div>}

      {/* What every run on this platform gets, before any workflow file. */}
      <EnvStore />

      {!!doc.problems?.length && (
        <div className="banner err">
          <strong>{doc.problems!.length} problem{doc.problems!.length > 1 ? 's' : ''}</strong>
          <ul style={{ margin: '6px 0 0', paddingLeft: 18 }}>
            {doc.problems!.map((p, i) => <li key={i}>{p}</li>)}
          </ul>
        </div>
      )}

      <div className="card">
        <h3>Default model</h3>
        <p className="muted">Every step that names no provider of its own runs on this.</p>
        <table className="kv">
          <tbody>
            <tr><th>model</th><td className="mono">{doc.default.model}</td></tr>
            <tr><th>endpoint</th><td className="mono">{doc.default.baseUrl} <span className="muted">({doc.default.style})</span></td></tr>
            <tr>
              <th>key</th>
              <td>
                <span className="mono">{doc.default.apiKeyEnv}</span>{' '}
                <span className={doc.default.keySet ? 'pill done' : 'pill failed'}>
                  {doc.default.keySet ? 'set' : 'NOT SET'}
                </span>
                <div className="hint">The variable's name. Its value is never read into the UI.</div>
              </td>
            </tr>
            <tr><th>platform</th><td className="mono">{doc.shell.os}/{doc.shell.arch}</td></tr>
            <tr>
              <th>shell</th>
              <td>
                {doc.shell.problem
                  ? <span className="pill failed">{doc.shell.problem}</span>
                  : <><span className="mono">{doc.shell.using}</span> <span className="muted">{doc.shell.path}</span></>}
                {!!doc.shell.available?.length && (
                  <div className="hint">
                    available: {doc.shell.available.join(', ')} — a <span className="mono">run</span> step
                    may name one with <span className="mono">shell:</span>
                  </div>)}
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <EntryTable
        title="Providers" entries={doc.providers} rows={providers}
        hint="A provider is where a step's turns come from. `http` is an endpoint; `cli` and `acp` are a program on this machine, which needs no key because the CLI holds its own."
        onDelete={name => act(`Removed provider ${name}.`, () => api.deleteProvider(name))}
      />
      <ProviderForm models={doc.models} onSave={p => act(`Saved provider ${p.name}.`, () => api.saveProvider(p))} />

      <EntryTable
        title="Classifiers" entries={doc.classifiers} rows={classifiers}
        hint="The judge tier: a cheap typed decision instead of a whole agent turn. A `judge` step names one."
        onDelete={name => act(`Removed classifier ${name}.`, () => api.deleteClassifier(name))}
      />
      <ClassifierForm onSave={c => act(`Saved classifier ${c.name}.`, () => api.saveClassifier(c))} />

      <div className="card">
        <div className="subhead"><h2>MCP servers</h2></div>
        <p className="muted" style={{ marginBottom: 12 }}>
          A step names one to gain its tools. Like every other registry entry, the name is
          what travels and the endpoint stays here.
        </p>
        <DataTable
          rows={mcp}
          columns={[
            { key: 'name', header: 'Name', width: 180, value: m => m.name, cell: m => <span className="mono" style={{ fontWeight: 500 }}>{m.name}</span> },
            { key: 'where', header: 'Command or URL', value: m => m.command || m.url || '', cell: m => <span className="mono muted">{m.command || m.url || '—'}</span> },
            { key: 'description', header: 'What it is for', value: m => m.description || '', cell: m => m.description || <span className="muted">—</span> },
          ]}
          getKey={m => m.name} initialSort={{ key: 'name', dir: 'asc' }}
          searchPlaceholder="Search MCP servers…"
          empty="None configured." />
      </div>

      <div className="card">
        <h3>Loaded</h3>
        <p className="muted">
          {doc.skills.count} skills · {doc.mcp.count} mcp servers · {doc.workflows.count} workflows
        </p>
        {!!skipped.length && (
          <Section title="Entries the loader refused" count={skipped.length}
            hint="A refused entry is not available to any step. This is usually why a name does not resolve.">
            <DataTable
              rows={skipped}
              columns={[
                { key: 'location', header: 'Entry', width: 260, value: s => s.location, cell: s => <span className="mono">{s.location}</span> },
                { key: 'reason', header: 'Why it was refused', value: s => s.reason, cell: s => s.reason },
              ]}
              getKey={s => s.location + s.reason} pageSize={10}
              searchPlaceholder="Search refused entries…" empty="Nothing was refused." />
          </Section>)}
        {!!doc.skills.skipped?.length && (
          <Section title="Skills the loader refused" count={doc.skills.skipped?.length ?? 0}
            hint="Usually a duplicate name: a root earlier in precedence already claimed it.">
            <DataTable
              rows={doc.skills.skipped.map(s => ({ s }))}
              columns={[{ key: 's', header: 'File', value: r => r.s, cell: r => <span className="mono">{r.s}</span> }]}
              getKey={r => r.s} pageSize={10}
              searchPlaceholder="Search refused skills…" empty="Every skill loaded." />
          </Section>)}
      </div>
    </>
  )
}

/** EntryTable joins the declared entry with the doctor's verdict on it, because
 *  neither half is useful alone: the entry says what was asked for, the verdict
 *  says whether this machine can deliver it. */
function EntryTable({ title, entries, rows, hint, onDelete }: {
  title: string
  entries: DoctorEntry[]
  rows: Array<{ name: string; description?: string }>
  hint: string
  onDelete: (name: string) => void
}) {
  const describe = new Map(rows.map(r => [r.name, r.description || '']))

  const columns: Column<DoctorEntry>[] = [
    {
      key: 'ready', header: '', width: 40, sortable: false,
      value: e => (e.ready ? 1 : 0),
      cell: e => <span style={{ color: e.ready ? 'var(--ok)' : 'var(--err)', fontWeight: 700 }}>{e.ready ? '✓' : '✗'}</span>,
    },
    { key: 'name', header: 'Name', width: 150, value: e => e.name, cell: e => <span className="mono" style={{ fontWeight: 500 }}>{e.name}</span> },
    { key: 'kind', header: 'Kind', width: 110, value: e => e.kind, cell: e => <span className="pill">{e.kind}</span> },
    { key: 'model', header: 'Model', width: 210, value: e => e.model || '', cell: e => e.model ? <span className="mono muted">{e.model}</span> : <span className="muted">—</span> },
    {
      key: 'detail', header: 'Where it resolves to',
      value: e => e.problem || e.detail || '',
      cell: e => (
        <>
          <span className={e.problem ? '' : 'mono muted'} style={e.problem ? { color: 'var(--err)' } : undefined}>
            {e.problem || e.detail}
          </span>
          {describe.get(e.name) && <div className="hint">{describe.get(e.name)}</div>}
        </>),
    },
    {
      key: 'actions', header: '', width: 90, align: 'right', sortable: false,
      cell: e => (
        <button className="ghost small danger-text" onClick={ev => {
          ev.stopPropagation()
          if (confirm(`Remove ${e.name}? A workflow still naming it will fail to load — which is deliberate, so you find out now.`)) onDelete(e.name)
        }}>Remove</button>),
    },
  ]

  const filters: Filter<DoctorEntry>[] = [
    { key: 'kind', label: 'kinds', options: [...new Set(entries.map(e => e.kind))].sort(), match: (e, v) => e.kind === v },
    { key: 'ready', label: 'states', options: ['ready', 'not ready'], match: (e, v) => (v === 'ready' ? e.ready : !e.ready) },
  ]

  return (
    <div className="card">
      <div className="subhead"><h2>{title}</h2></div>
      <p className="muted" style={{ marginBottom: 12 }}>{hint}</p>
      <DataTable rows={entries} columns={columns} filters={filters} getKey={e => e.name}
        rowClass={e => (e.ready ? '' : 'bad')}
        initialSort={{ key: 'name', dir: 'asc' }}
        searchPlaceholder={`Search ${title.toLowerCase()}…`}
        empty={`No ${title.toLowerCase()} configured.`} />
    </div>
  )
}

function ProviderForm({ models, onSave }: { models: string[]; onSave: (p: Provider) => void }) {
  const [p, setP] = useState<Provider>({ name: '', kind: 'http', style: 'openai', apiKeyEnv: 'OPENROUTER_API_KEY' })
  const set = (patch: Partial<Provider>) => setP({ ...p, ...patch })
  const local = p.kind === 'cli' || p.kind === 'acp'
  return (
    <div className="card">
      <h3>Add or replace a provider</h3>
      <div className="grid2">
        <Field label="Name" hint="What a step writes in `provider:`.">
          <input value={p.name} onChange={e => set({ name: e.target.value })} placeholder="haiku" />
        </Field>
        <Field label="Kind" hint={local
          ? 'The model is a program on this machine. No key: the CLI holds its own credential.'
          : 'An OpenAI- or Anthropic-style HTTP endpoint.'}>
          <select value={p.kind} onChange={e => set({ kind: e.target.value as Provider['kind'] })}>
            <option value="http">http — an endpoint</option>
            <option value="cli">cli — a coding-agent CLI, one shot per turn</option>
            <option value="acp">acp — a persistent agent process</option>
          </select>
        </Field>
        <Field label="Description"><input value={p.description || ''} onChange={e => set({ description: e.target.value })} /></Field>
        {!local && <>
          <Field label="Base URL"><input className="mono" value={p.baseUrl || ''} onChange={e => set({ baseUrl: e.target.value })} placeholder="https://openrouter.ai/api/v1" /></Field>
          <Field label="Style">
            <select value={p.style || 'openai'} onChange={e => set({ style: e.target.value })}>
              <option value="openai">openai</option><option value="anthropic">anthropic</option>
            </select>
          </Field>
          <Field label="Model" hint="A step may override this with `model:` and stay on this endpoint.">
            <input className="mono" list="known-models" value={p.model || ''} onChange={e => set({ model: e.target.value })} />
            <datalist id="known-models">{models.map(m => <option key={m} value={m} />)}</datalist>
          </Field>
          <Field label="API key variable"
            hint="The NAME of an environment variable — never a key. A value pasted here is refused by the API, because this file is meant to be committable.">
            <input className="mono" value={p.apiKeyEnv || ''} onChange={e => set({ apiKeyEnv: e.target.value })} placeholder="OPENROUTER_API_KEY" />
          </Field>
        </>}
        {local && <>
          <Field label="Preset" hint="devin, claude or copilot. Anything else needs an explicit command.">
            <select value={p.preset || ''} onChange={e => set({ preset: e.target.value, command: undefined })}>
              <option value="">— use a command instead —</option>
              <option value="devin">devin</option><option value="claude">claude</option><option value="copilot">copilot</option>
            </select>
          </Field>
          <Field label="Command" hint="argv for any other agent CLI, space separated. Wins over a preset.">
            <input className="mono" value={(p.command || []).join(' ')}
              onChange={e => set({ command: e.target.value.split(/\s+/).filter(Boolean) })} placeholder="opencode run" />
          </Field>
          <Field label="Model" hint="Passed to the CLI's model flag. Empty ⇒ the account default.">
            <input className="mono" value={p.model || ''} onChange={e => set({ model: e.target.value })} />
          </Field>
          <Field label="Timeout (s)"><input type="number" value={p.timeoutSec || 0} onChange={e => set({ timeoutSec: +e.target.value })} /></Field>
        </>}
      </div>
      <button disabled={!p.name} onClick={() => onSave(p)}>Save provider</button>
      <div className="hint">
        Saved through the loader's own validation, so the UI cannot create an entry that silently never loads.
      </div>
    </div>
  )
}

function ClassifierForm({ onSave }: { onSave: (c: Classifier) => void }) {
  const [c, setC] = useState<Classifier>({ name: '', backend: 'openrouter' })
  const set = (patch: Partial<Classifier>) => setC({ ...c, ...patch })
  return (
    <div className="card">
      <h3>Add or replace a classifier</h3>
      <div className="grid2">
        <Field label="Name" hint="What a `judge` step writes in `classifier:`.">
          <input value={c.name} onChange={e => set({ name: e.target.value })} placeholder="jev" />
        </Field>
        <Field label="Backend" hint="typesafe and openrouter set base, model and key variable as a unit — assembling them by hand is how you end up with one provider's model spelling against another's base.">
          <select value={c.backend} onChange={e => set({ backend: e.target.value })}>
            <option value="openrouter">openrouter</option>
            <option value="typesafe">typesafe</option>
            <option value="llm">llm — a chat model answering the same typed questions</option>
            <option value="static">static — a recorded answer, for tests</option>
          </select>
        </Field>
        <Field label="Description"><input value={c.description || ''} onChange={e => set({ description: e.target.value })} /></Field>
        <Field label="Model"><input className="mono" value={c.model || ''} onChange={e => set({ model: e.target.value })} /></Field>
        <Field label="Base URL"><input className="mono" value={c.baseUrl || ''} onChange={e => set({ baseUrl: e.target.value })} /></Field>
        <Field label="API key variable" hint="A variable's NAME, never a key.">
          <input className="mono" value={c.apiKeyEnv || ''} onChange={e => set({ apiKeyEnv: e.target.value })} />
        </Field>
      </div>
      <button disabled={!c.name} onClick={() => onSave(c)}>Save classifier</button>
    </div>
  )
}
