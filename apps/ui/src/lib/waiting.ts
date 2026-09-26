import { useEffect, useState } from 'react'
import { api, type Run } from '../api'

/** The two states in which a run has stopped ON PURPOSE for a person. */
export const WAITING = ['awaiting_approval', 'needs_input']

export function isWaiting(status: string) { return WAITING.includes(status) }

/** Every run waiting on a person, across every project, kept fresh.
 *
 *  Asked of the server BY STATUS, not picked out of the newest hundred runs: a
 *  pause from last night is exactly the one that matters and exactly the one a
 *  recency window would drop. One poll serves the whole app, so the top bar,
 *  the project list and the tab title can never disagree about the count. */
let listeners: Array<(r: Run[]) => void> = []
let latest: Run[] = []
let timer: ReturnType<typeof setInterval> | undefined

function poll() {
  api.runs({ status: WAITING })
    .then(r => { latest = r; listeners.forEach(f => f(r)) })
    .catch(() => { /* the next tick tries again; a blip is not worth a banner */ })
}

export function useWaiting(): Run[] {
  const [runs, setRuns] = useState<Run[]>(latest)
  useEffect(() => {
    listeners.push(setRuns)
    if (!timer) { poll(); timer = setInterval(poll, 5000) }
    return () => {
      listeners = listeners.filter(f => f !== setRuns)
      if (!listeners.length && timer) { clearInterval(timer); timer = undefined }
    }
  }, [])
  return runs
}
