import { useMemo } from 'react'
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

export default function WorkflowCanvas({ steps, active, onPick }: {
  steps: Step[]
  /** step id to highlight — the running step, or the one being edited */
  active?: string
  onPick?: (id: string) => void
}) {
  const g = useMemo(() => layout(steps), [steps])
  if (!steps.length) return <div className="muted">No steps yet.</div>

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
      </div>
      <svg viewBox={`0 0 ${g.width} ${g.height}`} width="100%" height={g.height} role="img">
        <defs>
          <marker id="arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
            <path d="M 0 0 L 10 5 L 0 10 z" className="arrowhead" />
          </marker>
        </defs>
        {g.edges.map((e, i) => (
          <path key={i} d={e.d} className={`edge${e.soft ? ' soft' : ''}`} markerEnd="url(#arrow)" />
        ))}
        {g.nodes.map(n => (
          <g key={n.id} transform={`translate(${n.x},${n.y})`}
            className={`node kind-${n.kind}${n.id === active ? ' active' : ''}${onPick ? ' clickable' : ''}`}
            onClick={onPick ? () => onPick(n.id) : undefined}>
            <rect width={NODE_W} height={NODE_H} rx="8" />
            <text x="12" y="20" className="nid">{n.id}</text>
            <text x="12" y="38" className="nkind">{n.kind}{n.provider ? ` · ${n.provider}` : ''}</text>
            {n.produces.length > 0 && <text x="12" y="55" className="nfact">⇒ {n.produces.join(', ')}</text>}
            {n.guarded && <text x={NODE_W - 12} y="20" className="nguard" textAnchor="end">?</text>}
          </g>))}
      </svg>
    </div>
  )
}

const NODE_W = 190
const NODE_H = 64
const GAP_X = 70
const GAP_Y = 22

type Node = {
  id: string; kind: string; provider?: string; produces: string[]; guarded: boolean
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
 *  points right and no edge has to be routed backwards. Cycles cannot occur —
 *  the loader rejects them — but the walk is depth-capped anyway, because a
 *  rendering bug should not become an infinite loop in somebody's browser. */
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

  const layerOf = new Map<string, number>()
  const resolve = (id: string, depth = 0): number => {
    if (layerOf.has(id)) return layerOf.get(id)!
    if (depth > steps.length) return 0
    const s = byId.get(id)
    const d = s ? deps(s) : []
    const l = d.length ? Math.max(...d.map(p => resolve(p, depth + 1))) + 1 : 0
    layerOf.set(id, l)
    return l
  }
  for (const s of steps) resolve(s.id)

  const rows = new Map<number, number>()
  const nodes: Node[] = steps.map(s => {
    const layer = layerOf.get(s.id) || 0
    const row = rows.get(layer) || 0
    rows.set(layer, row + 1)
    return {
      id: s.id, kind: kindOf(s), provider: s.provider,
      produces: s.produces || [], guarded: !!(s as any).when?.length,
      layer, x: layer * (NODE_W + GAP_X) + 10, y: row * (NODE_H + GAP_Y) + 10,
    }
  })
  const pos = new Map(nodes.map(n => [n.id, n]))

  const edges: Array<{ d: string; soft: boolean }> = []
  for (const s of steps) {
    const to = pos.get(s.id)!
    for (const depId of deps(s)) {
      const from = pos.get(depId)
      if (!from) continue
      const x1 = from.x + NODE_W, y1 = from.y + NODE_H / 2
      const x2 = to.x, y2 = to.y + NODE_H / 2
      const mx = (x1 + x2) / 2
      edges.push({ d: `M ${x1} ${y1} C ${mx} ${y1}, ${mx} ${y2}, ${x2} ${y2}`, soft: mode === 'facts' })
    }
  }

  const layers = Math.max(...nodes.map(n => n.layer)) + 1
  const tallest = Math.max(...[...rows.values()])
  return {
    nodes, edges, mode, alternatives,
    width: layers * (NODE_W + GAP_X) + 20,
    height: tallest * (NODE_H + GAP_Y) + 20,
  }
}
