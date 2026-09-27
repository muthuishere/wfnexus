import { describe, test, expect, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import { ModelPicker } from './components/builder/StepEditor'
import type { Doctor, Step } from './api'

// The provider list is what the server's doctor reports; only the LIVE model
// query is answered here, because the real one would start opencode.
const doctor = {
  default: { model: 'anthropic/claude-sonnet-4.5', baseUrl: '', style: 'openai', apiKeyEnv: 'OPENROUTER_API_KEY', keySet: true },
  providers: [
    { name: 'opencode-acp', kind: 'acp', model: 'opencode/mimo-v2.6-flash-free', ready: true, detail: '/opt/homebrew/bin/opencode' },
    { name: 'devin', kind: 'acp', ready: true },
    { name: 'gemini-cli', kind: 'cli', ready: false, problem: 'gemini is not on PATH' },
  ],
  classifiers: [], storage: { driver: 'sqlite', artifacts: 'folder' }, skills: { count: 0 }, mcp: { count: 0 }, workflows: { count: 0 }, models: [],
} as unknown as Doctor

let asked: string[] = []
let real: typeof fetch
beforeEach(() => {
  asked = []
  real = globalThis.fetch
  globalThis.fetch = ((input: any, init?: any) => {
    const m = String(input).match(/^\/api\/providers\/([^/]+)\/models$/)
    if (!m) return real(input, init)
    asked.push(decodeURIComponent(m[1]))
    return Promise.resolve(new Response(JSON.stringify({
      provider: m[1], current: 'opencode/big-pickle', configured: 'opencode/mimo-v2.6-flash-free', offered: true,
      models: [
        { id: 'opencode/big-pickle', name: 'Big Pickle', free: false },
        { id: 'opencode/mimo-v2.6-flash-free', name: 'MiMo', free: true },
        { id: 'opencode/nemotron-3-ultra-free', name: 'Nemotron', free: true },
      ],
    }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
  }) as typeof fetch
})
afterEach(() => { globalThis.fetch = real; cleanup() })

function Harness({ initial }: { initial: Step }) {
  let step = initial
  const r = render(<ModelPicker step={step} doctor={doctor} onChange={p => { step = { ...step, ...p }; r.rerender(<ModelPicker step={step} doctor={doctor} onChange={() => {}} />) }} />)
  return r
}

describe('the provider and model dropdowns', () => {
  test('what this server can run is offered; what it cannot is shown but not selectable', () => {
    Harness({ initial: { id: 's' } as Step })
    const ready = screen.getByRole('option', { name: /opencode-acp · acp/ }) as HTMLOptionElement
    const missing = screen.getByRole('option', { name: /gemini-cli/ }) as HTMLOptionElement
    expect(ready.disabled).toBe(false)
    expect(missing.disabled).toBe(true)
    expect(asked).toEqual([])   // nothing is asked until an acp provider is chosen
  })

  test("an acp provider's model is a dropdown of what its agent offers, free ones marked", async () => {
    Harness({ initial: { id: 's', provider: 'opencode-acp' } as Step })
    await screen.findByText(/3 models, 2 free/)
    expect(asked).toEqual(['opencode-acp'])
    expect(screen.getByRole('option', { name: '★ free · opencode/mimo-v2.6-flash-free' })).toBeTruthy()
    fireEvent.click(screen.getByLabelText(/free only/))
    await waitFor(() => expect(screen.queryByRole('option', { name: 'opencode/big-pickle' })).toBeNull())
    expect(screen.getAllByRole('option', { name: /★ free/ }).length).toBe(2)
  })
})
