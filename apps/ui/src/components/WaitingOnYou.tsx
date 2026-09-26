import { useEffect, useRef, useState } from 'react'
import { useWaiting } from '../lib/waiting'
import { href, shortName } from '../lib/routes'
import { summariseRun } from '../lib/workflow'
import { ago } from '../lib/time'

/** The top bar's "waiting on you". A paused run is an action somebody owes,
 *  and it used to be visible only to whoever happened to open that run — with
 *  no notifier configured, a run that paused at 02:00 waited until someone
 *  looked. Now it is on every page, in the tab title too, so a background tab
 *  says so.
 *
 *  It lists and links; it does not approve. Approving happens on the run's
 *  page, where what the step will do is in front of the person deciding. */
export default function WaitingOnYou() {
  const runs = useWaiting()
  const [open, setOpen] = useState(false)
  const box = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const base = document.title.replace(/^\(\d+\) /, '')
    document.title = runs.length ? `(${runs.length}) ${base}` : base
  }, [runs.length])

  useEffect(() => {
    if (!open) return
    const close = (e: MouseEvent) => { if (!box.current?.contains(e.target as Node)) setOpen(false) }
    const esc = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false) }
    addEventListener('mousedown', close); addEventListener('keydown', esc)
    return () => { removeEventListener('mousedown', close); removeEventListener('keydown', esc) }
  }, [open])

  if (!runs.length) return null
  return (
    <div className="waitbox" ref={box}>
      <button className="waitpill" onClick={() => setOpen(o => !o)} aria-expanded={open}>
        <span className="dot awaiting_approval" /> {runs.length} waiting on you
      </button>
      {open && (
        <div className="waitmenu" role="menu">
          {runs.map(r => {
            const p = r.project || 'local'
            const about = summariseRun(r)
            return (
              <a key={r.id} role="menuitem" href={href.run(p, shortName(r.workflow), r.id)} onClick={() => setOpen(false)}>
                <span className={`badge ${r.status}`}>{r.status === 'needs_input' ? 'question' : 'approval'}</span>
                <span className="waitwhat">
                  <b className="mono">{shortName(r.workflow)}</b>
                  {r.currentStep && <span className="muted"> · {r.currentStep}</span>}
                  <span className="waitsub">{p}{about ? ` · ${about}` : ''}</span>
                </span>
                <span className="muted waitwhen">{ago(r.updatedAt || r.createdAt)}</span>
              </a>)
          })}
        </div>)}
    </div>
  )
}
