import { useEffect, useState, type FormEvent } from 'react'
import { api, onUnauthenticated, type SignInStart, type WhoAmI } from '../api'

// WHO the browser is talking to the server as — and, when it is nobody, how to
// fix that. Off loopback the card offers a username and password (ADR 0025);
// beside it, the terminal ways in (ADR 0017, 0022). All of them end in the same
// HttpOnly cookie the page never sees:
//   - the password form posts to /api/auth/password;
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
    // A first sign-in (or an admin reset) must set a new password before
    // anything else; the server refuses every other call until it is done.
    if (who.mustChange) return <ChangePasswordCard forced onDone={() => location.reload()} />
    const signOut = () => { api.signOut().catch(() => {}).finally(() => location.reload()) }
    return (
      <span className="muted" style={{ fontSize: 'var(--fs-2xs)', display: 'inline-flex', gap: 8, alignItems: 'center' }}
        title={who.role ? `${who.kind} · ${who.role}` : who.kind}>
        {who.name}
        {who.kind === 'user' && who.authRequired ? <a className="ghost small" href="#/password">Password</a> : null}
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
        <h2>Sign in</h2>
        <PasswordForm />

        <h3 style={{ marginTop: 18 }}>Or sign in from your terminal</h3>
        <p className="muted" style={{ marginBottom: 10 }}>
          A terminal that is already signed in can let this browser in without a password.
        </p>

        <div style={{ fontSize: 'var(--fs-sm)', fontWeight: 600 }}>On the machine running wfx, open the UI signed in:</div>
        <Command text="wfx ui" />

        <div style={{ fontSize: 'var(--fs-sm)', fontWeight: 600 }}>Or approve this browser from any signed-in terminal:</div>
        {grant ? (
          <>
            <div className="mono" data-testid="signin-code"
              style={{ fontSize: 22, letterSpacing: '.15em', margin: '6px 0 0' }}>{grant.user_code}</div>
            <Command text={`wfx login --approve ${grant.user_code}`} />
          </>
        ) : (
          <p className="muted" style={{ fontSize: 'var(--fs-2xs)', margin: '6px 0 14px' }}>
            {state === 'unavailable' ? 'Could not get a sign-in code from the server.' : 'Getting a code…'}
          </p>
        )}

        <div className="muted" style={{ fontSize: 'var(--fs-2xs)', marginBottom: 12 }}>
          No terminal signed in yet? Start with <span className="mono">wfx login --url {origin}</span>.
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
          <span className="muted" style={{ fontSize: 'var(--fs-2xs)' }}>
            {state === 'denied' ? 'The sign-in was denied.' : grant ? 'Waiting for approval — this page signs in by itself.' : ''}
          </span>
          {locked && !grant ? <span className="muted" style={{ fontSize: 'var(--fs-2xs)' }}>{locked}</span> : null}
        </div>
      </div>
    </div>
  )
}

/** Username and password (ADR 0025). An unknown user and a wrong password get
 *  the same message from the server, and this shows it as given. */
function PasswordForm() {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const submit = (e: FormEvent) => {
    e.preventDefault()
    setBusy(true); setError('')
    api.passwordSignIn(username, password).then(r => {
      if (r.ok) { location.reload(); return }
      setPassword(''); setError(r.error || 'Sign-in failed'); setBusy(false)
    }, () => { setError('Could not reach the server'); setBusy(false) })
  }
  return (
    <form onSubmit={submit} aria-label="Sign in with a password" style={{ display: 'grid', gap: 8, margin: '8px 0 4px' }}>
      <label>Username
        <input name="username" autoComplete="username" value={username} onChange={e => setUsername(e.target.value)} required />
      </label>
      <label>Password
        <input name="password" type="password" autoComplete="current-password" value={password} onChange={e => setPassword(e.target.value)} required />
      </label>
      {error ? <div role="status" className="danger-text" style={{ fontSize: 'var(--fs-sm)' }}>{error}</div> : null}
      <div><button type="submit" disabled={busy || !username || !password}>{busy ? 'Signing in…' : 'Sign in'}</button></div>
    </form>
  )
}

/** Set a new password, proving the current one. `forced` is the must-change
 *  screen after a first sign-in or an admin reset: nothing else is reachable
 *  until it is done, so it is a full-screen card with only a sign-out beside it. */
export function ChangePasswordCard({ forced, onDone }: { forced?: boolean; onDone?: () => void }) {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [again, setAgain] = useState('')
  const [error, setError] = useState('')
  const [done, setDone] = useState(false)
  const tooShort = next.length > 0 && [...next].length < MIN_PASSWORD
  const mismatch = again.length > 0 && again !== next
  const submit = (e: FormEvent) => {
    e.preventDefault()
    setError('')
    api.changePassword(current, next).then(() => {
      setDone(true); setCurrent(''); setNext(''); setAgain('')
      onDone?.()
    }, err => setError(err instanceof Error ? err.message : String(err)))
  }
  const form = (
    <form onSubmit={submit} aria-label="Change password" style={{ display: 'grid', gap: 8, maxWidth: 420 }}>
      {forced ? (
        <p className="muted">This is a one-time password. Choose your own before you continue.</p>
      ) : null}
      <label>Current password
        <input name="current" type="password" autoComplete="current-password" value={current} onChange={e => setCurrent(e.target.value)} required />
      </label>
      <label>New password ({MIN_PASSWORD}+ characters)
        <input name="new" type="password" autoComplete="new-password" value={next} onChange={e => setNext(e.target.value)} required />
      </label>
      <label>New password again
        <input name="again" type="password" autoComplete="new-password" value={again} onChange={e => setAgain(e.target.value)} required />
      </label>
      {tooShort ? <div className="muted" style={{ fontSize: 'var(--fs-2xs)' }}>At least {MIN_PASSWORD} characters.</div> : null}
      {mismatch ? <div className="muted" style={{ fontSize: 'var(--fs-2xs)' }}>The two new passwords differ.</div> : null}
      {error ? <div role="status" className="danger-text" style={{ fontSize: 'var(--fs-sm)' }}>{error}</div> : null}
      {done && !forced ? <div role="status" className="muted">Password changed.</div> : null}
      <div style={{ display: 'flex', gap: 8 }}>
        <button type="submit" disabled={!current || !next || tooShort || mismatch || again !== next}>Change password</button>
        {forced ? (
          <button type="button" className="ghost" onClick={() => { api.signOut().catch(() => {}).finally(() => location.reload()) }}>Sign out</button>
        ) : null}
      </div>
    </form>
  )
  if (!forced) return form
  return (
    <div role="alert" style={{
      position: 'fixed', inset: 0, zIndex: 100, display: 'flex',
      alignItems: 'center', justifyContent: 'center', padding: 16,
      background: 'rgba(20, 32, 58, .45)',
    }}>
      <div className="card" style={{ maxWidth: 480, width: '100%' }}>
        <h2>Set a new password</h2>
        {form}
      </div>
    </div>
  )
}

/** The server's rule (auth.MinPasswordLen); the server enforces it, this only
 *  says so before the round trip. */
const MIN_PASSWORD = 12
