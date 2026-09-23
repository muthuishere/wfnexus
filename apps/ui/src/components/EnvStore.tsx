import { useCallback, useEffect, useState } from 'react'
import { api, type EnvList } from '../api'

/** The platform's env store, for one scope.
 *
 *  Used twice: system-wide on the System page, and per repository on a Project
 *  page. The layering is system → project → workflow → job → step, so what is
 *  set here is what a run gets BEFORE the workflow file says anything.
 *
 *  A secret's value is written once and never shown again — not here, not in
 *  the API, not in the CLI. That is not a UI choice: the value is not sent. */
export default function EnvStore({ project }: { project?: string }) {
  const [list, setList] = useState<EnvList>()
  const [err, setErr] = useState('')
  const [adding, setAdding] = useState(false)

  const load = useCallback(() => {
    api.env(project).then(l => { setList(l); setErr('') })
      .catch(e => setErr(e instanceof Error ? e.message : String(e)))
  }, [project])
  useEffect(load, [load])

  const where = project ? <>project <span className="mono">{project}</span></> : 'this platform'

  return (
    <div className="card">
      <div className="subhead">
        <h2>Environment</h2>
        <button className="ghost small" onClick={() => setAdding(a => !a)}>
          {adding ? 'Cancel' : '+ variable'}
        </button>
      </div>
      <p className="muted" style={{ marginBottom: 12 }}>
        What every run on {where} gets, before the workflow file says anything.
        Layering is <span className="mono">system → project → workflow → job → step</span>.
        Values are encrypted at rest; a secret is written once and never shown again.
      </p>

      {err && <div className="banner err">{err}</div>}
      {adding && <AddVar project={project} onDone={() => { setAdding(false); load() }} onError={setErr} />}

      {!list ? <div className="muted">loading…</div>
        : list.vars.length === 0
          ? <div className="muted">Nothing set.</div>
          : <table className="rows"><tbody>
            {list.vars.map(v => (
              <tr key={v.key}>
                <td className="mono" style={{ width: 240, fontWeight: 550 }}>{v.key}</td>
                <td style={{ width: 90 }}>
                  {v.secret ? <span className="pill">secret</span>
                    : <span className="muted" style={{ fontSize: 12 }}>plain</span>}
                </td>
                <td className="mono muted">
                  {v.secret ? <span title="the value is not sent to this page">••••••••</span> : v.value}
                </td>
                <td style={{ width: 80 }}>
                  <button className="ghost small danger-text" onClick={() => {
                    if (!confirm(`Remove ${v.key}? Any step relying on it will fail on the missing variable.`)) return
                    api.deleteEnv(v.key, project).then(load).catch(e => setErr(String(e.message || e)))
                  }}>Remove</button>
                </td>
              </tr>))}
          </tbody></table>}

      {list?.keySource && (
        <p className="muted" style={{ fontSize: 12, marginTop: 12 }}>
          Encrypted with the key from <span className="mono">{list.keySource}</span>.
          Lose it and every value here is unreadable — back it up somewhere other than the database.
        </p>)}
    </div>
  )
}

function AddVar({ project, onDone, onError }: { project?: string; onDone: () => void; onError: (s: string) => void }) {
  const [key, setKey] = useState('')
  const [value, setValue] = useState('')
  const [secret, setSecret] = useState(true)
  const [busy, setBusy] = useState(false)

  const save = () => {
    setBusy(true)
    api.setEnv({ key: key.trim(), value, secret }, project)
      .then(() => { setKey(''); setValue(''); onDone() })
      .catch(e => onError(String(e.message || e)))
      .finally(() => setBusy(false))
  }

  return (
    <div style={{ display: 'flex', gap: 8, alignItems: 'center', marginBottom: 12, flexWrap: 'wrap' }}>
      <input className="mono" placeholder="NAME" value={key} style={{ width: 220 }}
        onChange={e => setKey(e.target.value)} />
      <input className="mono" placeholder="value" value={value} style={{ flex: 1, minWidth: 240 }}
        // A secret is a password field: it should not be shoulder-readable, and
        // it should not land in a browser's form autofill.
        type={secret ? 'password' : 'text'} autoComplete="off"
        onChange={e => setValue(e.target.value)} />
      <label className="muted" style={{ fontSize: 13, display: 'flex', gap: 6, alignItems: 'center' }}>
        <input type="checkbox" checked={secret} onChange={e => setSecret(e.target.checked)} />
        secret
      </label>
      <button disabled={busy || !key.trim() || !value} onClick={save}>{busy ? 'Saving…' : 'Save'}</button>
      <span className="muted" style={{ fontSize: 12, flexBasis: '100%' }}>
        Untick <em>secret</em> only for ordinary configuration — a URL, a flag — which stays readable here.
      </span>
    </div>
  )
}
