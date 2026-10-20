import { useEffect, useMemo, useState } from 'react'
import type { Step } from '../api'

/** WorkflowCanvas draws the execution shape of a workflow.
 *
 *  The shape is not authored, so it has to be derived — which is exactly why a
 *  picture earns its place here:
 *
 *  - `needs:` gives explicit edges, GitHub-Actions style (ADR 0012).
 *  - `consumes:`/`produces:` give NO edges at all: the order is derived from
 *    facts and re-derived after every step (ADR 0014). Two steps producing the
 *    same fact are alternatives chosen by their guards at runtime, which is
 *    impossible to see in a list of steps and obvious in a diagram.
 *  - Neither means plain sequence.
 *
 *  Hand-rolled SVG rather than a graph library: the layout is a layering, the
 *  node count is small, and a dependency that renders boxes would be a poor
 *  trade for one view. */
type Mode = 'needs' | 'facts' | 'sequence'
type Zoom = 'fit' | 'full'

export default function WorkflowCanvas({ steps, active, onPick, status }: {
  steps: Step[]
  /** step id to highlight — the running step, or the one being edited */
  active?: string
  onPick?: (id: string) => void
  /** step id → run status (done, running, failed, …), on the run page */
  status?: Record<string, string>
}) {
  const g = useMemo(() => layout(steps), [steps])
  // A callback ref, not useRef + [] deps: the box does not exist while there
  // are no steps (the early return below), and a builder draft gains its first
  // step later — the observer must attach whenever the box appears.
  const [box, setBox] = useState<HTMLDivElement | null>(null)
  const [avail, setAvail] = useState({ w: 0, h: 0 })
  const [zoom, setZoom] = useState<Zoom>('full')
  useEffect(() => {
    if (!box || typeof ResizeObserver === 'undefined') return
    const measure = () => setAvail({ w: box.clientWidth, h: Math.round(window.innerHeight * 0.7) })
    measure()
    const ro = new ResizeObserver(measure)
    ro.observe(box)
    return () => ro.disconnect()
  }, [box])
  if (!steps.length) return <div className="muted">No steps yet.</div>

  // 100% is the default and the readable size: never squeezed to the width,
  // the smallest label is 13px, and a long chain is laid out top-to-bottom
  // (see layout) so it scrolls down the page instead of shrinking to dots.
  // 'fit' is an opt-in OVERVIEW — the whole graph in the box, however small —
  // offered only when the graph does not fit at 100%.
  const overflows = avail.w > 0 && (g.width > avail.w || g.height > avail.h)
  const scale = zoom === 'fit' && overflows ? Math.min(1, avail.w / g.width, avail.h / g.height) : 1

  return (
    <div className="canvas">
      <div className="canvashead">
        <span className="pill">{g.mode === 'needs' ? 'DAG — from `needs:`'
          : g.mode === 'facts' ? 'derived — from `consumes:`/`produces:`'
            : 'sequential'}</span>
        {g.mode === 'facts' && <span className="hint">
          Order is re-derived after every step; a dashed edge is one possible route, not a fixed one.
        </span>}
        {g.alternatives.length > 0 && <span className="hint">
          alternatives for the same fact: {g.alternatives.join(', ')} — guards pick one at runtime
        </span>}
        {overflows && <span className="canvaszoom">
          <button className={zoom === 'full' ? 'on' : ''} onClick={() => setZoom('full')}
            title="readable size; scroll to see the rest">100%</button>
          <button className={zoom === 'fit' ? 'on' : ''} onClick={() => setZoom('fit')}
            title="the whole graph at once — labels may be small">fit</button>
        </span>}
      </div>
      <div className="canvasbody" ref={setBox} data-scale={scale.toFixed(3)}>
        <svg viewBox={`0 0 ${g.width} ${g.height}`} width={g.width * scale} height={g.height * scale}
          role="img" aria-label={`${steps.length} steps, ${g.vertical ? 'top to bottom' : 'left to right'}`}
          data-orientation={g.vertical ? 'vertical' : 'horizontal'}>
          <defs>
            <marker id="arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
              <path d="M 0 0 L 10 5 L 0 10 z" className="arrowhead" />
            </marker>
          </defs>
          {g.edges.map((e, i) => (
            <path key={i} d={e.d} className={`edge${e.soft ? ' soft' : ''}`} markerEnd="url(#arrow)" />
          ))}
          {g.nodes.map(n => {
            const st = status?.[n.id]
            return (
              <g key={n.id} transform={`translate(${n.x},${n.y})`} data-step={n.id}
                className={`node kind-${n.kind}${st ? ` st-${st}` : ''}${n.id === active ? ' active' : ''}${onPick ? ' clickable' : ''}`}
                onClick={onPick ? () => onPick(n.id) : undefined}>
                <title>{n.name !== n.id ? `${n.name} (${n.id})` : n.id}</title>
                <rect width={NODE_W} height={NODE_H} rx="8" />
                <text x="12" y="22" className="nname">{clip(n.name, 24)}</text>
                <text x="12" y="42" className="nkind">{clip(`${n.id} · ${n.kind}${n.provider ? ` · ${n.provider}` : ''}`, 28)}</text>
                {(st || n.produces.length > 0) && <text x="12" y="60" className={st ? 'nstatus' : 'nfact'}>
                  {st ? st.replace(/_/g, ' ') : clip(`⇒ ${n.produces.join(', ')}`, 28)}</text>}
                {n.guarded && <text x={NODE_W - 12} y="22" className="nguard" textAnchor="end">?</text>}
              </g>)
          })}
        </svg>
      </div>
    </div>
  )
}

function clip(s: string, n: number) { return s.length > n ? s.slice(0, n - 1) + '…' : s }

const NODE_W = 220
const NODE_H = 70
const GAP_X = 60
const GAP_Y = 34
/** more layers than this and the graph is drawn top-to-bottom */
export const MAX_LAYERS_ACROSS = 5

type Node = {
  id: string; name: string; kind: string; provider?: string; produces: string[]; guarded: boolean
  x: number; y: number; layer: number
}

function kindOf(s: Step): string {
  if (s.run) return 'run'
  if (s.decide) return 'judge'
  return 'prompt'
}

/** layout assigns each step a layer and turns the dependencies into paths.
 *
 *  A step's layer is one past the deepest thing it depends on, so an edge always
 *  points forward (right, or down when the graph is drawn top-to-bottom) and no
 *  edge has to be routed backwards. Cycles cannot occur —
 *  the loader rejects them — but a builder draft can hold one mid-edit, so the
 *  walk breaks cycles (see resolve) rather than looping in somebody's browser. */
function layout(steps: Step[]) {
  const byId = new Map(steps.map(s => [s.id, s]))
  const usesNeeds = steps.some(s => s.needs?.length)
  const usesFacts = steps.some(s => s.produces?.length || s.consumes?.length)
  const mode: Mode = usesNeeds ? 'needs' : usesFacts ? 'facts' : 'sequence'

  // producers of each fact, for the derived mode
  const producers = new Map<string, string[]>()
  for (const s of steps) for (const f of s.produces || []) {
    producers.set(f, [...(producers.get(f) || []), s.id])
  }
  const alternatives = [...producers.entries()].filter(([, ids]) => ids.length > 1).map(([f]) => f)

  const deps = (s: Step): string[] => {
    if (mode === 'needs') return (s.needs || []).filter(id => byId.has(id))
    if (mode === 'facts') {
      const out = new Set<string>()
      for (const f of s.consumes || []) for (const p of producers.get(f) || []) if (p !== s.id) out.add(p)
      return [...out]
    }
    const i = steps.indexOf(s)
    return i > 0 ? [steps[i - 1].id] : []
  }

  // Every step is resolved once (memoized), and a step met again while it is
  // still being resolved is a cycle: it counts as layer 0 there instead of
  // being walked again. A saved workflow cannot have one — the loader rejects
  // it — but a builder DRAFT can, mid-edit, and a depth cap alone made each
  // cycle re-walk every path: exponential, a frozen tab.
  const layerOf = new Map<string, number>()
  const visiting = new Set<string>()
  const resolve = (id: string): number => {
    if (layerOf.has(id)) return layerOf.get(id)!
    if (visiting.has(id)) return 0
    visiting.add(id)
    const s = byId.get(id)
    const d = s ? deps(s) : []
    const l = d.length ? Math.max(...d.map(p => resolve(p))) + 1 : 0
    visiting.delete(id)
    layerOf.set(id, l)
    return l
  }
  for (const s of steps) resolve(s.id)

  const layers = Math.max(...steps.map(s => layerOf.get(s.id) || 0)) + 1
  // Left-to-right reads best while it fits a screen; past that every layer
  // becomes a row, so a 30-step chain is 30 rows of readable boxes.
  const vertical = layers > MAX_LAYERS_ACROSS
  const rows = new Map<number, number>()
  const slot = new Map<string, number>()
  for (const s of steps) {
    const layer = layerOf.get(s.id) || 0
    const i = rows.get(layer) || 0
    rows.set(layer, i + 1)
    slot.set(s.id, i)
  }
  const widest = Math.max(...[...rows.values()])
  const nodes: Node[] = steps.map(s => {
    const layer = layerOf.get(s.id) || 0
    const i = slot.get(s.id)!
    // centre each layer on the widest one, so a chain runs straight down
    const off = (widest - (rows.get(layer) || 1)) / 2
    return {
      id: s.id, name: s.name || s.id, kind: kindOf(s), provider: s.provider,
      produces: s.produces || [], guarded: !!(s as any).when?.length, layer,
      x: vertical ? (i + off) * (NODE_W + GAP_X) + 10 : layer * (NODE_W + GAP_X) + 10,
      y: vertical ? layer * (NODE_H + GAP_Y) + 10 : i * (NODE_H + GAP_Y) + 10,
    }
  })
  const pos = new Map(nodes.map(n => [n.id, n]))

  const edges: Array<{ d: string; soft: boolean }> = []
  for (const s of steps) {
    const to = pos.get(s.id)!
    for (const depId of deps(s)) {
      const from = pos.get(depId)
      if (!from) continue
      if (vertical) {
        const x1 = from.x + NODE_W / 2, y1 = from.y + NODE_H
        const x2 = to.x + NODE_W / 2, y2 = to.y
        const my = (y1 + y2) / 2
        edges.push({ d: `M ${x1} ${y1} C ${x1} ${my}, ${x2} ${my}, ${x2} ${y2}`, soft: mode === 'facts' })
      } else {
        const x1 = from.x + NODE_W, y1 = from.y + NODE_H / 2
        const x2 = to.x, y2 = to.y + NODE_H / 2
        const mx = (x1 + x2) / 2
        edges.push({ d: `M ${x1} ${y1} C ${mx} ${y1}, ${mx} ${y2}, ${x2} ${y2}`, soft: mode === 'facts' })
      }
    }
  }

  const across = vertical ? widest : layers
  const down = vertical ? layers : widest
  return {
    nodes, edges, mode, alternatives, vertical,
    width: across * (NODE_W + GAP_X) - GAP_X + 20,
    height: down * (NODE_H + GAP_Y) - GAP_Y + 20,
  }
}
