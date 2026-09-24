import { test, expect, afterEach } from 'vitest'
import { render, cleanup } from '@testing-library/react'
import { Cost, correctionsOf, dur, tokens } from './components/RunTimeline'
import { readableCommand } from './components/EventLog'
import type { Event } from './api'

afterEach(() => cleanup())

// Every payload below is VERBATIM from a run on this machine — the ids are in
// the comments so the claim can be re-checked against the database.

// ADR 0020. Three states, three renderings, and the one that matters most is
// the TRUE zero: `local-proof` ran on qwen3:4b on this machine and cost
// nothing, which is a feature and must never read as a missing number.
test('free, priced, unpriced and untracked each render differently', () => {
  // run 154e6b7f — qwen3:4b via ollama-http
  const { container: free } = render(<Cost usage={{ costUsd: 0, totalTokens: 1647 }} />)
  expect(free.querySelector('.cost.free')).toBeTruthy()
  expect(free.textContent).toContain('$0.00')
  expect(free.textContent).toContain('free')

  const { container: paid } = render(<Cost usage={{ costUsd: 4.370577 }} />)
  expect(paid.textContent).toBe('$4.37')
  expect(paid.querySelector('.cost.free')).toBeNull()

  // run 7629577d, step `unpriced.nodefault` — real tokens, no price configured
  const { container: unknown } = render(<Cost usage={{ costUnknown: true, totalTokens: 1787 }} />)
  expect(unknown.querySelector('.cost.unknown')).toBeTruthy()
  expect(unknown.textContent).toMatch(/unknown/)
  expect(unknown.textContent).not.toContain('$')   // the whole point

  // run 7437e287 — older than cost accounting: tokens, and nothing else
  const { container: old } = render(<Cost usage={{ totalTokens: 1287416 }} />)
  expect(old.textContent).toMatch(/not tracked/)
  expect(old.textContent).not.toContain('$')
})

// The hero moment: the contract refused an answer and the model fixed it.
// Captured from run 70ca673e-03ae-461c-ba38-923eced1e86b, step `check`.
test('a refused submit_output is paired with the turn that corrected it', () => {
  const events = [
    { id: 3561, kind: 'tool_call', stepId: 'check', payload: { name: 'submit_output', args: { ok: 'yes', verdict: 'maybe' }, turn: 0 } },
    { id: 3562, kind: 'tool_result', stepId: 'check', payload: { name: 'submit_output', isError: true, output: "output does not match the required schema:\n- ok: got string, want boolean\n- verdict: value must be one of 'pass', 'fail'" } },
    { id: 3566, kind: 'tool_call', stepId: 'check', payload: { name: 'submit_output', args: { ok: true, verdict: 'pass' }, turn: 1 } },
    { id: 3567, kind: 'tool_result', stepId: 'check', payload: { name: 'submit_output', isError: false, output: 'accepted' } },
  ] as unknown as Event[]
  const c = correctionsOf(events)
  expect(c).toHaveLength(1)
  expect(c[0].fixedTurn).toBe(1)
  expect(c[0].why).toContain('want boolean')
})

// The bash tool is handed the step environment inline, so the command on the
// wire is not the command a human would read. Verbatim from run 7437e287.
test('the WFX_ exports are stripped from a displayed command', () => {
  const raw = "WFX_PROJECT='local' WFX_RUN_ID='7437e287-35a5-4100-a773-cab056d0bda5' WFX_STEP_ID='survey' " +
    "WFX_WORKSPACE='/Users/x/worktree' git diff main~3..main"
  expect(readableCommand(raw)).toBe('git diff main~3..main')
  expect(readableCommand('git status')).toBe('git status')
})

test('durations and token counts stay readable at run scale', () => {
  expect(dur(41_000)).toBe('41s')
  expect(dur(176_000)).toBe('2m 56s')
  expect(tokens(1_424_154)).toBe('1.42M')
})
