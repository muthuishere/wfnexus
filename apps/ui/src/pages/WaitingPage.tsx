import { useWaiting } from '../lib/waiting'
import { href, shortName } from '../lib/routes'
import { summariseRun } from '../lib/workflow'
import { headlineOf } from '../lib/story'
import { ago } from '../lib/time'

/** Every run paused for a person, across every project. Approving happens on
 *  the run's page, where what the step will do is in front of the decider. */
export default function WaitingPage() {
  const runs = useWaiting()
  return (
    <div>
      <div className="subhead"><h1>Waiting on you</h1><span className="muted">{runs.length} run{runs.length === 1 ? '' : 's'}</span></div>
      <div className="card">
        {runs.length === 0 && <p className="muted">Nothing is waiting on you.</p>}
        {runs.map(r => (
          <a key={r.id} className="hrow" href={href.run(r.project || 'local', r.workflow, r.id)}>
            <span><span className="badge awaiting_approval">{r.status === 'needs_input' ? 'question' : 'approval'}</span></span>
            <span className="mono">{shortName(r.workflow)}</span>
            <span className="h">{headlineOf(r).text}{summariseRun(r) ? ` · ${summariseRun(r)}` : ''}</span>
            <span className="muted ago">{ago(r.updatedAt || r.createdAt)}</span>
          </a>))}
      </div>
    </div>
  )
}
