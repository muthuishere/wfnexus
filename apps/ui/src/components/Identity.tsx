import { useEffect, useState } from 'react'
import { api, onUnauthenticated, type SignInStart, type WhoAmI } from '../api'

// WHO the browser is talking to the server as — and, when it is nobody, how to
// fix that. Sign-in is approved from a TERMINAL (ADR 0017, 0022), so this
// component never collects a credential. It gives the browser a session in one
// of two ways, both ending in an HttpOnly cookie the page never sees:
//   - `wfx ui` in a signed-in terminal opens a one-time link that signs this
//     browser in directly (what a person on the server's own machine uses);
//   - this card starts a device grant and shows its code; the person runs
//     `wfx login --approve <code>` and the card's poll gets the cookie.
//
// Three states, and the first one is the point:
//   loopback, not authenticated → render NOTHING. On the solo install auth is
//     ABSENT rather than satisfied, so the UI must look exactly as it did
//     before identity existed: no name, no badge, no "signed out" chrome.
//   authenticated → the name, quietly, in the corner of the top bar.
//   401 from any call (or off-loopback with no credential) → the sign-in card.
//
// This is NOT the actor claim in api.ts. That records WHO approved a paused
// step and is remembered in localStorage with no server involvement; this is
// whether the server accepted a credential.
export default function Identity() {
  const [who, setWho] = useState<WhoAmI | null>(null)
  const [locked, setLocked] = useState('')

  useEffect(() => {
    // whoami is the one call that answers rather than 401s on loopback, so the
    // solo install learns it is loopback without tripping the card below.
    api.whoami().then(setWho).catch(() => { /* the 401 listener has it */ })
    return onUnauthenticated(message => setLocked(message))
  }, [])

  const needsLogin = !!locked || (who ? !who.authenticated && !who.loopback : false)

  if (!needsLogin) {
    if (!who?.authenticated) return null   // the solo install: no chrome at all
    const signOut = () => { api.signOut().catch(() => {}).finally(() => location.reload()) }
    return (
      <span className="muted" style={{ fontSize: 12, display: 'inline-flex', gap: 8, alignItems: 'center' }}
        title={who.role ? `${who.kind} · ${who.role}` : who.kind}>
        {who.name}
        {who.kind === 'user' ? <button className="ghost small" onClick={signOut}>Sign out</button> : null}
      </span>
    )
  }
  return <SignInCard locked={locked} />
}

function Command({ text }: { text: string }) {
  const [copied, setCopied] = useState(false)
  const copy = () => {
    navigator.clipboard?.writeText(text).then(
      () => { setCopied(true); setTimeout(() => setCopied(false), 1500) },
      () => { /* no clipboard permission — the command is on screen to select */ },
    )
  }
  return (
    <div
      className="mono"
      style={{
        display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap',
        background: 'var(--raised)', border: '1px solid var(--border)',
        borderRadius: 'var(--radius)', padding: '10px 12px', margin: '6px 0 14px',
        overflowWrap: 'anywhere',
      }}
    >
      <span style={{ flex: '1 1 200px' }}>{text}</span>
      <button className="ghost small" onClick={copy}>{copied ? 'Copied' : 'Copy'}</button>
    </div>
  )
}

function SignInCard({ locked }: { locked: string }) {
  const [grant, setGrant] = useState<SignInStart | null>(null)
  const [state, setState] = useState('')

  // Start a grant and poll it at the server's pace (RFC 8628 §3.5). A new
  // grant replaces an expired one, so a card left open keeps a live code.
  useEffect(() => {
    let stopped = false
    let timer: ReturnType<typeof setTimeout> | undefined
    const start = () => {
      api.startSignIn().then(g => {
        if (stopped) return
        setGrant(g); setState('')
        let interval = Math.max(g.interval, 1) * 1000
        const deadline = Date.now() + g.expires_in * 1000
        const poll = () => {
          if (stopped) return
          if (Date.now() > deadline) { start(); return }
          api.pollSignIn(g.device_code).then(r => {
            if (stopped) return
            if (r === 'signed_in') { setState('signed_in'); location.reload(); return }
            if (r === 'slow_down') interval += 5000
            if (r === 'access_denied') { setState('denied'); return }
            if (r === 'expired_token') { start(); return }
            timer = setTimeout(poll, interval)
          }, () => { if (!stopped) timer = setTimeout(poll, interval) })
        }
        timer = setTimeout(poll, interval)
      }, () => { if (!stopped) setState('unavailable') })
    }
    start()
    return () => { stopped = true; if (timer) clearTimeout(timer) }
  }, [])

  const origin = location.origin
  return (
    <div
      role="alert"
      style={{
        position: 'fixed', inset: 0, zIndex: 100, display: 'flex',
        alignItems: 'center', justifyContent: 'center', padding: 16,
        background: 'rgba(20, 32, 58, .45)',
      }}
    >
      <div className="card" style={{ maxWidth: 480, width: '100%' }}>
        <h2>Sign in from your terminal</h2>
        <p className="muted" style={{ marginBottom: 10 }}>
          This server needs a credential. The browser never takes a password or token —
          a terminal that is already signed in lets it in.
        </p>

        <div style={{ fontSize: 13, fontWeight: 600 }}>On the machine running wfx, open the UI signed in:</div>
        <Command text="wfx ui" />

        <div style={{ fontSize: 13, fontWeight: 600 }}>Or approve this browser from any signed-in terminal:</div>
        {grant ? (
          <>
            <div className="mono" data-testid="signin-code"
              style={{ fontSize: 22, letterSpacing: '.15em', margin: '6px 0 0' }}>{grant.user_code}</div>
            <Command text={`wfx login --approve ${grant.user_code}`} />
          </>
        ) : (
          <p className="muted" style={{ fontSize: 12, margin: '6px 0 14px' }}>
            {state === 'unavailable' ? 'Could not get a sign-in code from the server.' : 'Getting a code…'}
          </p>
        )}

        <div className="muted" style={{ fontSize: 12, marginBottom: 12 }}>
          No terminal signed in yet? Start with <span className="mono">wfx login --url {origin}</span>.
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
          <span className="muted" style={{ fontSize: 12 }}>
            {state === 'denied' ? 'The sign-in was denied.' : grant ? 'Waiting for approval — this page signs in by itself.' : ''}
          </span>
          {locked && !grant ? <span className="muted" style={{ fontSize: 12 }}>{locked}</span> : null}
        </div>
      </div>
    </div>
  )
}
