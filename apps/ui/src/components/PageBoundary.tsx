import { Component, type ReactNode } from 'react'

/** PageBoundary keeps a rendering bug on one page from blanking the whole app.
 *  A page that throws — a field shaped differently than the UI expected — shows
 *  what broke and the way out, and the side nav keeps working. A new route
 *  (`resetKey`) clears the error — without remounting a healthy page, which
 *  would throw away its state on every in-page hash change. */
export default class PageBoundary extends Component<{ children: ReactNode; resetKey?: string }, { err?: Error }> {
  state: { err?: Error } = {}
  static getDerivedStateFromError(err: Error) { return { err } }
  componentDidUpdate(prev: { resetKey?: string }) {
    if (this.state.err && prev.resetKey !== this.props.resetKey) this.setState({ err: undefined })
  }
  componentDidCatch(err: Error) { console.error('page crashed:', err) }
  render() {
    if (!this.state.err) return this.props.children
    return (
      <div className="card" role="alert">
        <b>This page hit a bug and could not be drawn.</b>
        <pre style={{ whiteSpace: 'pre-wrap', marginTop: 8 }}>{this.state.err.message}</pre>
        <button type="button" onClick={() => this.setState({ err: undefined })}>Try again</button>
      </div>)
  }
}
