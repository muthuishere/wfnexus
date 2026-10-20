import { test, expect, afterEach } from 'vitest'
import { render, cleanup } from '@testing-library/react'
import WorkflowCanvas, { MIN_SCALE } from './components/WorkflowCanvas'
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
  // drawn at its own pixel size, never width="100%"
  const [, , vw, vh] = svg.getAttribute('viewBox')!.split(' ').map(Number)
  expect(Number(svg.getAttribute('width'))).toBeGreaterThanOrEqual(vw * MIN_SCALE)
  expect(Number(svg.getAttribute('height'))).toBeGreaterThanOrEqual(vh * MIN_SCALE)
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
  expect(MIN_SCALE * 13).toBeGreaterThanOrEqual(12) // the smallest label never drops below 12px
})

test('a short chain stays left-to-right', () => {
  const { container } = render(<WorkflowCanvas steps={chain(3)} />)
  expect(container.querySelector('svg')!.dataset.orientation).toBe('horizontal')
})
