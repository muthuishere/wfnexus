import { beforeAll } from 'vitest'

// The components fetch relative paths ('/api/…'), which is right in a browser
// and meaningless to Node's fetch. jsdom sets the document's origin but not
// Node's URL resolution, so this resolves relative requests against the test
// server — the ONE thing standing between the real components and the real
// API. Everything else about the request is untouched.
const base = `http://127.0.0.1:${process.env.WFX_E2E_PORT || 8433}`
const real = globalThis.fetch

beforeAll(() => {
  globalThis.fetch = ((input: any, init?: any) => {
    if (typeof input === 'string' && input.startsWith('/')) return real(base + input, init)
    return real(input, init)
  }) as typeof fetch
})

// jsdom has no EventSource, and the run page opens one for its live activity
// feed. This is a minimal one over fetch: ~30 lines we control, because both
// the `eventsource` package's class and a subclass of it delivered events whose
// `data` was undefined under jsdom, and a harness that mangles the payload is
// worse than no harness. It implements only what the page uses — named events,
// onmessage and close.
class TestEventSource {
  private ctrl = new AbortController()
  onmessage: ((e: { data: string }) => void) | null = null
  private listeners = new Map<string, ((e: { data: string }) => void)[]>()
  constructor(url: string) {
    const abs = url.startsWith('/') ? base + url : url
    void this.pump(abs)
  }
  addEventListener(name: string, fn: (e: { data: string }) => void) {
    this.listeners.set(name, [...(this.listeners.get(name) ?? []), fn])
  }
  removeEventListener() { /* the page only ever closes */ }
  close() { this.ctrl.abort() }
  private async pump(url: string) {
    let res: Response
    try { res = await fetch(url, { signal: this.ctrl.signal }) } catch { return }
    const reader = res.body?.getReader()
    if (!reader) return
    const dec = new TextDecoder()
    let buf = ''
    for (;;) {
      let chunk
      try { chunk = await reader.read() } catch { return }
      if (chunk.done) return
      buf += dec.decode(chunk.value, { stream: true })
      let i
      while ((i = buf.indexOf('\n\n')) !== -1) {
        const frame = buf.slice(0, i)
        buf = buf.slice(i + 2)
        let name = 'message', data = ''
        for (const line of frame.split('\n')) {
          if (line.startsWith('event:')) name = line.slice(6).trim()
          else if (line.startsWith('data:')) data += line.slice(5).trim()
        }
        if (!data) continue // a `: ping` keepalive carries none
        const ev = { data }
        for (const fn of this.listeners.get(name) ?? []) fn(ev)
        if (name === 'message') this.onmessage?.(ev)
      }
    }
  }
}
beforeAll(() => { globalThis.EventSource = TestEventSource as unknown as typeof EventSource })

// jsdom implements no layout, so scrollIntoView does not exist. The activity
// feed calls it to follow the newest line. A no-op is honest here: this is a
// gap in jsdom, not a fault in the page, and nothing about scrolling is what
// these tests assert.
beforeAll(() => { Element.prototype.scrollIntoView = () => {} })
