import type { FileInfo } from '../api'

/** THE FILES THAT TRAVEL WITH A WORKFLOW, shown wherever it is listed.
 *
 *  A workflow is the YAML *and* whatever sits beside it — the run.js a step
 *  invokes, a fixture, a README. Listing only the name is what makes a gallery
 *  look reusable and not be: you copy it, and the first run fails on a script
 *  nobody mentioned.
 *
 *  So nothing here filters. A .sql the platform has no opinion about is listed
 *  exactly like the script it does understand, because the person reading the
 *  list is the one who knows which of them matters.
 *
 *  One shape, used by the Builder, the gallery and a project's table, so the
 *  files always read the same way. */
export default function FileList({ files, empty }: { files?: FileInfo[]; empty?: string }) {
  if (!files?.length) return <span className="muted" style={{ fontSize: 12 }}>{empty ?? '—'}</span>
  return (
    <ul className="mono" style={{ fontSize: 12, margin: 0, paddingLeft: 0, listStyle: 'none' }}>
      {files.map(f => (
        <li key={f.path} style={{ display: 'flex', gap: 10, justifyContent: 'space-between' }}>
          <span>{f.path}</span>
          <span className="muted">{bytes(f.size)}</span>
        </li>))}
    </ul>
  )
}

/** Sizes a person reads at a glance. A sidecar is a script or a fixture, so
 *  the interesting range is bytes to a few hundred kB. */
export function bytes(n?: number): string {
  if (n === undefined) return ''
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(n < 10240 ? 1 : 0)} kB`
  return `${(n / (1024 * 1024)).toFixed(1) } MB`
}
