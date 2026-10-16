import type { ReactNode } from 'react'

// A deliberately SMALL markdown renderer for step summaries: headings, lists,
// paragraphs, fenced code, and inline **bold**, `code` and [links](url). It
// builds React elements — never HTML strings — so a summary a script wrote
// cannot inject markup into the page.

function inline(text: string, key: string): ReactNode[] {
  const out: ReactNode[] = []
  const re = /(\*\*[^*]+\*\*|`[^`]+`|\[[^\]]+\]\([^)\s]+\))/g
  let last = 0, m: RegExpExecArray | null, i = 0
  while ((m = re.exec(text))) {
    if (m.index > last) out.push(text.slice(last, m.index))
    const t = m[0], k = `${key}-${i++}`
    if (t.startsWith('**')) out.push(<b key={k}>{t.slice(2, -2)}</b>)
    else if (t.startsWith('`')) out.push(<code key={k}>{t.slice(1, -1)}</code>)
    else {
      const lm = /^\[([^\]]+)\]\(([^)\s]+)\)$/.exec(t)!
      const safe = /^(https?:|#|\/)/.test(lm[2])
      out.push(safe ? <a key={k} href={lm[2]} target="_blank" rel="noreferrer">{lm[1]}</a> : lm[1])
    }
    last = m.index + t.length
  }
  if (last < text.length) out.push(text.slice(last))
  return out
}

export default function Markdown({ text }: { text: string }) {
  const lines = text.replace(/\r/g, '').split('\n')
  const blocks: ReactNode[] = []
  let i = 0
  while (i < lines.length) {
    const l = lines[i]
    const k = `b${i}`
    if (l.trim().startsWith('```')) {
      const body: string[] = []
      i++
      while (i < lines.length && !lines[i].trim().startsWith('```')) body.push(lines[i++])
      i++
      blocks.push(<pre key={k}>{body.join('\n')}</pre>)
      continue
    }
    const h = /^(#{1,6})\s+(.*)$/.exec(l)
    if (h) {
      blocks.push(<div key={k} className={`md-h md-h${Math.min(h[1].length, 3)}`}>{inline(h[2], k)}</div>)
      i++
      continue
    }
    if (/^\s*([-*+]|\d+\.)\s+/.test(l)) {
      const items: ReactNode[] = []
      const ordered = /^\s*\d+\./.test(l)
      while (i < lines.length && /^\s*([-*+]|\d+\.)\s+/.test(lines[i])) {
        items.push(<li key={i}>{inline(lines[i].replace(/^\s*([-*+]|\d+\.)\s+/, ''), `l${i}`)}</li>)
        i++
      }
      blocks.push(ordered ? <ol key={k}>{items}</ol> : <ul key={k}>{items}</ul>)
      continue
    }
    if (!l.trim()) { i++; continue }
    const para: string[] = []
    while (i < lines.length && lines[i].trim() && !/^(#{1,6}\s|```|\s*([-*+]|\d+\.)\s)/.test(lines[i])) para.push(lines[i++])
    blocks.push(<p key={k}>{inline(para.join(' '), k)}</p>)
  }
  return <div className="md">{blocks}</div>
}
