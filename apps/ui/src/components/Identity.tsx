import { useEffect, useState } from 'react'
import { api, onUnauthenticated, type WhoAmI } from '../api'

// WHO the browser is talking to the server as — and, when it is nobody, how to
// fix that. Login is a CLI flow (RFC 8628 device grant, `wfx login`), so this
// component never collects a credential; there is deliberately no browser login
// form, and inventing one here would be inventing a second way in.
//
// Three states, and the first one is the point:
//   loopback, not authenticated → render NOTHING. On the solo install auth is
//     ABSENT rather than satisfied, so the UI must look exactly as it did
//     before identity existed: no name, no badge, no "signed out" chrome.
//   authenticated → the name, quietly, in the corner of the top bar.
//   401 from any call (or off-loopback with no credential) → one panel saying
//     which command to run in the terminal.
//
// This is NOT the actor claim in api.ts. That records WHO approved a paused
// step and is remembered in localStorage with no server involvement; this is
// whether the server accepted a credential. Conflating them would let a browser
// prompt look like a login.
export default function Identity() {
  const [who, setWho] = useState<WhoAmI | null>(null)
  const [locked, setLocked] = useState('')
  const [copied, setCopied] = useState(false)

  useEffect(() => {
    // whoami is the one call that answers rather than 401s, so the loopback
    // install learns it is loopback without tripping the notice below.
    api.whoami().then(setWho).catch(() => { /* the 401 listener has it */ })
    return onUnauthenticated(message => setLocked(message))
  }, [])

  const command = `wfx login --url ${location.origin}`
  const needsLogin = !!locked || (who ? !who.authenticated && !who.loopback : false)

  const copy = () => {
    navigator.clipboard?.writeText(command).then(
      () => { setCopied(true); setTimeout(() => setCopied(false), 1500) },
      () => { /* no clipboard permission — the command is on screen to select */ },
    )
  }

  if (!needsLogin) {
    if (!who?.authenticated) return null   // the solo install: no chrome at all
    return (
      <span className="muted" style={{ fontSize: 12 }} title={who.role ? `${who.kind} · ${who.role}` : who.kind}>
        {who.name}
      </span>
    )
  }

  return (
    <div
      role="alert"
      style={{
        position: 'fixed', inset: 0, zIndex: 100, display: 'flex',
        alignItems: 'center', justifyContent: 'center', padding: 16,
        background: 'rgba(20, 32, 58, .45)',
      }}
    >
      <div className="card" style={{ maxWidth: 460, width: '100%' }}>
        <h2>Sign in from your terminal</h2>
        <p className="muted" style={{ marginBottom: 12 }}>
          This server needs a credential. Logging in happens in a terminal, not in the
          browser — run this, approve it, then reload this page.
        </p>
        <div
          className="mono"
          style={{
            display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap',
            background: 'var(--raised)', border: '1px solid var(--border)',
            borderRadius: 'var(--radius)', padding: '10px 12px', marginBottom: 12,
            overflowWrap: 'anywhere',
          }}
        >
          <span style={{ flex: '1 1 200px' }}>{command}</span>
          <button className="ghost small" onClick={copy}>{copied ? 'Copied' : 'Copy'}</button>
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
          <button onClick={() => location.reload()}>Reload</button>
          {locked ? <span className="muted" style={{ fontSize: 12 }}>{locked}</span> : null}
        </div>
      </div>
    </div>
  )
}
