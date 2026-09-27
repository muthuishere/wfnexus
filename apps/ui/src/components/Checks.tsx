import type { Run, Workflow } from '../api'
import { triggerNames } from '../lib/workflow'
import Icon from './Icon'

type Check = { id: string; label: string; ok: boolean | 'warn'; detail: string }

/** What can be said about a workflow from its definition and its history,
 *  without running it: each line a pass, a warning or a fail, with the reason.
 *  Exported for tests: the list is the logic, the table is only its view. */
export function checksFor(w: Workflow, runs: Run[]): Check[] {
  const noContract = w.steps.filter(s => !s.outputSchema || (!s.outputSchema.type && !s.outputSchema.properties)).map(s => s.id)
  const gated = w.steps.some(s => s.requiresApproval || (s.gates?.length ?? 0) > 0 || !!(s as { decide?: unknown }).decide)
  const recent = runs.slice(0, 20)
  const failed = recent.filter(r => r.status === 'failed').length
  return [
    { id: 'loads', label: 'Loads and validates', ok: true, detail: 'the server loaded this definition' },
    { id: 'describe', label: 'Says what it is for', ok: !!w.description?.trim() || 'warn', detail: w.description ? 'has a description' : 'no description' },
    { id: 'trigger', label: 'Has a trigger', ok: triggerNames(w).length > 0, detail: triggerNames(w).join(', ') || 'nothing starts it' },
    {
      id: 'contract', label: 'Every step has an output contract', ok: noContract.length === 0 || 'warn',
      detail: noContract.length ? `no output_schema on ${noContract.join(', ')}` : `${w.steps.length} of ${w.steps.length} steps typed`,
    },
    { id: 'gate', label: 'A human or a gate decides', ok: gated || 'warn', detail: gated ? 'an approval or gate is wired' : 'no approval step or gate' },
    {
      id: 'recent', label: 'Recent runs succeed', ok: recent.length === 0 ? 'warn' : failed === 0 ? true : failed * 2 < recent.length ? 'warn' : false,
      detail: recent.length ? `${recent.length - failed} of the last ${recent.length} did not fail` : 'never run',
    },
  ]
}

export default function Checks({ workflow, runs }: { workflow: Workflow; runs: Run[] }) {
  const list = checksFor(workflow, runs)
  return (
    <div className="card flush">
      <table className="rows checks"><tbody>
        {list.map(c => (
          <tr key={c.id} data-check={c.id} data-ok={String(c.ok)}>
            <td style={{ width: 28 }} className={c.ok === true ? 'ok' : c.ok === 'warn' ? 'warn' : 'err'}>
              <Icon name={c.ok === true ? 'check' : c.ok === 'warn' ? 'warn' : 'x'} size={16} />
            </td>
            <td>{c.label}</td>
            <td className="muted">{c.detail}</td>
          </tr>))}
      </tbody></table>
    </div>
  )
}
