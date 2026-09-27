import { test, expect, afterEach } from 'vitest'
import { render, cleanup } from '@testing-library/react'
import StepHarness from './components/StepHarness'
import type { Step } from './api'

afterEach(cleanup)

// The Steps tab once read "greetgreet": the name and the id sat side by side
// with nothing between them. The id is only worth showing when it says
// something the name does not.
test('a step whose id equals its name shows it once', () => {
  const { container } = render(<StepHarness step={{ id: 'greet', name: 'greet' } as Step} index={0} />)
  expect(container.querySelector('.titleline')!.textContent).toBe('greet')
})

test('a step with a distinct id shows both, as separate elements', () => {
  const { container } = render(<StepHarness step={{ id: 'greet', name: 'Say hello' } as Step} index={0} />)
  const line = container.querySelector('.titleline')!
  expect(line.querySelector('b')!.textContent).toBe('Say hello')
  expect(line.querySelector('.mono')!.textContent).toBe('greet')
})
