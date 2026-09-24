import { describe, test, expect, beforeAll, afterEach } from 'vitest'
import { render, screen, cleanup, waitFor, within, fireEvent } from '@testing-library/react'
import App from './App'

// The builder, mounted for real against the real server.
//
// The output contract used to be a row of a dozen controls per property,
// stacked down a page that showed every step at once. It is now the schema
// itself, as JSON, in one textarea, in a pane that shows one step. What has to
// hold: the sidebar selects, valid JSON commits, invalid JSON is REPORTED
// rather than applied — the step keeps the contract it had — and the server's
// verdict, which is the only thing that COMPILES the schema, reaches the page.
const thrown: unknown[] = []
beforeAll(() => {
  window.addEventListener('error', e => thrown.push(e.error ?? e.message))
  window.addEventListener('unhandledrejection', e => thrown.push(e.reason))
})
afterEach(() => { cleanup(); expect(thrown).toEqual([]) })

const go = (hash: string) => { window.location.hash = hash; return render(<App />) }

describe('the workflow builder', () => {
  const box = (slot: string) => screen.getByLabelText(slot) as HTMLTextAreaElement
  const type = (el: HTMLTextAreaElement, v: string) => fireEvent.change(el, { target: { value: v } })
  const yaml = () => (document.querySelector('.yamlpane pre') as HTMLElement).textContent || ''

  async function open() {
    go('#/workflows/deterministic/edit')
    await screen.findByLabelText('step:0')
  }

  test('the sidebar selects one step, and only that step is in the pane', async () => {
    await open()
    const showing = () => (document.querySelector('.stepcard .stepid') as HTMLInputElement).value
    expect(showing()).toBe('greet')

    const nav = document.querySelector('.stepnav') as HTMLElement
    fireEvent.click(within(nav).getByText('count'))
    await waitFor(() => expect(showing()).toBe('count'))
  })

  test('valid JSON commits, and the YAML preview is of the whole file', async () => {
    await open()
    expect(yaml()).toContain('id: greet')
    expect(yaml()).toContain('id: count')

    type(box('step:0'), '{"type":"object","properties":{"verdict":{"type":"string"}}}')
    await waitFor(() => expect(yaml()).toContain('verdict'))
  })

  test('invalid JSON says where, blocks the save, and keeps the last valid schema', async () => {
    await open()
    type(box('step:0'), '{"type":"object","properties":{"keeper":{"type":"string"}}}')
    await waitFor(() => expect(yaml()).toContain('keeper'))

    type(box('step:0'), '{"type":"object",,}')
    await screen.findAllByText(/line 1, column/)
    // The committed contract is untouched. This is the whole point.
    expect(yaml()).toContain('keeper')
    await waitFor(() => expect(screen.getByText(/problems? to fix/)).toBeTruthy())

    // Fixing it releases both.
    type(box('step:0'), '{"type":"object","properties":{"fixed":{"type":"string"}}}')
    await waitFor(() => expect(yaml()).toContain('fixed'))
    expect(screen.queryAllByText(/line 1, column/)).toHaveLength(0)
  })

  test('the server compiles the schema, and what it says lands on the page', async () => {
    await open()
    // Syntactically perfect JSON, and not a JSON Schema. Nothing in the
    // browser knows that; the compiler on the server does.
    type(box('step:0'), '{"type":"nonsense","properties":{"x":{"type":"string"}}}')
    await screen.findByText(/The loader would refuse this/, {}, { timeout: 10_000 })
    await screen.findByText(/output_schema is not a valid JSON Schema/)
  })
})
