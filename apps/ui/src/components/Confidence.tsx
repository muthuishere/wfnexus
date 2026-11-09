/** How sure an agent was — every agent step's output carries `confidence`
 *  (0-1) and `why` (engine/confidence.go). Below the step's bar (default 0.6)
 *  the run waits for a person. */

export type Confident = { confidence?: number; why?: string }

/** The confidence an output reports, or undefined when it reports none. */
export function confidenceOf(output: unknown): Confident | undefined {
  if (!output || typeof output !== 'object') return undefined
  const o = output as Record<string, unknown>
  if (typeof o.confidence !== 'number') return undefined
  return { confidence: o.confidence, why: typeof o.why === 'string' ? o.why : undefined }
}

/** The engine's pause reason "low confidence 0.42 < 0.60: why" as data. */
export function confidenceFromReason(reason?: string): Confident | undefined {
  const m = /^low confidence ([\d.]+) < [\d.]+: (.*)$/s.exec(reason || '')
  return m ? { confidence: parseFloat(m[1]), why: m[2] } : undefined
}

export function confidenceTone(c: number) {
  return c >= 0.8 ? 'var(--ok, #1a7f37)' : c >= 0.6 ? 'var(--warn, #b26a00)' : 'var(--err, #cf222e)'
}

export default function Confidence({ value, why, compact }: { value?: number; why?: string; compact?: boolean }) {
  if (value === undefined || value === null || Number.isNaN(value)) return null
  const tone = confidenceTone(value)
  return (
    <span className="confidence" title={why || ''} style={{ display: 'inline-flex', alignItems: 'baseline', gap: 6 }}>
      <span style={{ border: `1px solid ${tone}`, color: tone, borderRadius: 999, padding: '0 8px', fontWeight: 600, fontSize: 'var(--fs-xs)', whiteSpace: 'nowrap' }}>
        {Math.round(value * 100)}% sure
      </span>
      {!compact && why && <span className="muted" style={{ fontSize: 'var(--fs-xs)' }}>{why}</span>}
    </span>
  )
}
