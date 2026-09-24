import { useCallback, useEffect, useState } from 'react'
import { api, type StateVar } from '../api'

/** What a workflow REMEMBERS between runs.
 *
 *  Shown on the workflow page, because the whole debugging story is "why does
 *  this scheduled workflow think it is up to date?" — and the answer is a
 *  watermark somebody wrote three runs ago. Without this table that value is
 *  invisible and the only way to see it is a database client.
 *
 *  Four scopes, narrowest first: step, workflow, project, global. They are four
 *  separate namespaces, not a cascade — a key missing from one is NOT inherited
 *  from a wider one.
 *
 *  Values are shown in full because state is plaintext. It is not a secret
 *  store; `wfx env` is. */
export default function StateStore({ workflow, project }: { workflow?: string; project?: string }) {
  const [vars, setVars] = useState<StateVar[]>()
  const [err, setErr] = useState('')

  const load = useCallback(() => {
    const p = workflow ? api.workflowState(workflow).then(r => r.vars) : api.state('project', project || '').then(r => r.vars)
    p.then(v => { setVars(v); setErr('') }).catch(e => setErr(e instanceof Error ? e.message : String(e)))
  }, [workflow, project])
  useEffect(load, [load])

  const remove = (v: StateVar) => {
    if (!confirm(`Forget ${v.key}? The next run starts as if it had never been written.`)) return
    api.deleteState(v.scope, v.scopeName || '', v.key).then(load).catch(e => setErr(String(e.message || e)))
  }

  return (
    <div className="card">
      <div className="subhead"><h2>State</h2></div>
      <p className="muted" style={{ marginBottom: 12 }}>
        What survives a run. Read it in any template as{' '}
        <span className="mono">{'{{ .Workflow.key }}'}</span> — also{' '}
        <span className="mono">.Step</span>, <span className="mono">.Project</span> and{' '}
        <span className="mono">.Global</span>. Four separate namespaces: a key missing from one is
        not inherited from another. Write it with <span className="mono">wfx state set</span> or a
        step's <span className="mono">state:</span> block. Stored in plaintext — this is not a
        secret store.
      </p>

      {err && <div className="banner err">{err}</div>}

      {!vars ? <div className="muted">loading…</div>
        : vars.length === 0
          ? <div className="muted">Nothing remembered yet.</div>
          : <table className="rows"><tbody>
            {vars.map(v => (
              <tr key={`${v.scope}/${v.scopeName}/${v.key}`}>
                <td style={{ width: 150 }}>
                  <span className="pill">{v.scope}</span>{' '}
                  {v.scopeName && <span className="muted mono" style={{ fontSize: 11 }}>{v.scopeName}</span>}
                </td>
                <td className="mono" style={{ width: 220, fontWeight: 550 }}>{v.key}</td>
                <td className="mono muted">{v.value}</td>
                <td style={{ width: 80 }}>
                  <button className="ghost small danger-text" onClick={() => remove(v)}>Forget</button>
                </td>
              </tr>))}
          </tbody></table>}
    </div>
  )
}
