/** A short, readable "3m ago".
 *
 *  A run list is read to see what is happening NOW, and an ISO timestamp makes
 *  that arithmetic the reader's job. The exact time is kept in the cell's
 *  `title`, so nothing is lost — and the column sorts on the raw value, not on
 *  this string.
 */
export function ago(iso?: string): string {
  if (!iso) return '—'
  const s = Math.floor((Date.now() - new Date(iso).getTime()) / 1000)
  if (s < 0 || Number.isNaN(s)) return '—'
  if (s < 45) return 'just now'
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ago`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ago`
  const d = Math.floor(h / 24)
  return d < 30 ? `${d}d ago` : new Date(iso).toLocaleDateString()
}
