import { test, expect, afterEach, vi } from 'vitest'
import { render, cleanup, act, fireEvent } from '@testing-library/react'
import WorkflowCanvas from './components/WorkflowCanvas'
import type { Step } from './api'

afterEach(() => cleanup())

const chain = (n: number): Step[] => Array.from({ length: n }, (_, i) => ({
  id: `s${i + 1}`, name: `Step number ${i + 1}`, needs: i ? [`s${i}`] : [],
} as unknown as Step))

// reqsume-guardian is a 30-step `needs:` chain. It used to be one 30-layer row
// squeezed to the card width — boxes about 1% of their size, labels unreadable.
test('a 30-step chain is drawn top-to-bottom at readable size, with names and status', () => {
  const steps = chain(30)
  const { container } = render(<WorkflowCanvas steps={steps}
    status={{ s1: 'done', s2: 'running', s3: 'failed' }} />)
  const svg = container.querySelector('svg')!
  expect(svg.dataset.orientation).toBe('vertical')
  // drawn at its own pixel size (100% is the default), never width="100%"
  const [, , vw, vh] = svg.getAttribute('viewBox')!.split(' ').map(Number)
  expect(Number(svg.getAttribute('width'))).toBe(vw)
  expect(Number(svg.getAttribute('height'))).toBe(vh)
  expect(vh).toBeGreaterThan(vw * 10) // a column, not a row

  const nodes = container.querySelectorAll('g.node')
  expect(nodes).toHaveLength(30)
  // one box per row, each further down than the last
  const ys = [...nodes].map(n => Number(/translate\([\d.]+,([\d.]+)\)/.exec(n.getAttribute('transform')!)![1]))
  ys.slice(1).forEach((y, i) => expect(y).toBeGreaterThan(ys[i]))

  expect(nodes[0].querySelector('.nname')!.textContent).toBe('Step number 1')
  expect(nodes[0].classList).toContain('st-done')
  expect(nodes[1].querySelector('.nstatus')!.textContent).toBe('running')
  expect(nodes[2].classList).toContain('st-failed')
})

test('a short chain stays left-to-right', () => {
  const { container } = render(<WorkflowCanvas steps={chain(3)} />)
  expect(container.querySelector('svg')!.dataset.orientation).toBe('horizontal')
})

// A builder draft can hold a cycle mid-edit. The layering used to re-walk every
// path through it (depth-capped, not memoized): exponential — 20 steps that all
// need each other froze the tab.
test('a cyclic draft lays out at once instead of freezing the tab', () => {
  const ids = Array.from({ length: 20 }, (_, i) => `s${i}`)
  const steps = ids.map(id => ({ id, name: id, needs: ids.filter(x => x !== id) } as unknown as Step))
  const t0 = performance.now()
  const { container } = render(<WorkflowCanvas steps={steps} />)
  expect(performance.now() - t0).toBeLessThan(1000)
  expect(container.querySelectorAll('g.node')).toHaveLength(20)
})

// The box is measured whenever it appears — a draft starts with no steps (no
// box rendered) and gains them later; a [] effect never measured it.
test('the zoom toggle measures a box that appears after the first render, and fit is a real overview', () => {
  const observed: Element[] = []
  vi.stubGlobal('ResizeObserver', class { constructor(_: unknown) {} observe(el: Element) { observed.push(el) } disconnect() {} })
  const w = vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(600)
  try {
    const { container, rerender } = render(<WorkflowCanvas steps={[]} />)
    expect(container.textContent).toContain('No steps yet')
    rerender(<WorkflowCanvas steps={chain(30)} />)
    expect(observed).toHaveLength(1)
    const body = container.querySelector('.canvasbody') as HTMLElement
    expect(body.dataset.scale).toBe('1.000') // 100% by default
    const fit = [...container.querySelectorAll('.canvaszoom button')].find(b => b.textContent === 'fit')!
    act(() => { fireEvent.click(fit) })
    // the whole 30-row column inside 70% of the window height
    const svg = container.querySelector('svg')!
    expect(Number(svg.getAttribute('height'))).toBeLessThanOrEqual(Math.round(window.innerHeight * 0.7) + 1)
    expect(Number(body.dataset.scale)).toBeLessThan(0.5)
  } finally { w.mockRestore(); vi.unstubAllGlobals() }
})
