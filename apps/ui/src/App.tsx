import { useEffect, useState } from 'react'
import WorkflowsPage from './pages/WorkflowsPage'
import NewRunPage from './pages/NewRunPage'
import RunPage from './pages/RunPage'
import RunsPage from './pages/RunsPage'

// tiny hash router: #/workflows · #/workflows/:name/new · #/runs · #/runs/:id
function useRoute() {
  const [h, setH] = useState(location.hash || '#/runs')
  useEffect(() => { const f = () => setH(location.hash || '#/runs'); addEventListener('hashchange', f); return () => removeEventListener('hashchange', f) }, [])
  return h.replace(/^#/, '').split('/').filter(Boolean)
}

export default function App() {
  const r = useRoute()
  let page = <RunsPage />
  if (r[0] === 'workflows' && r[2] === 'new') page = <NewRunPage name={r[1]} />
  else if (r[0] === 'workflows') page = <WorkflowsPage />
  else if (r[0] === 'runs' && r[1]) page = <RunPage id={r[1]} />
  return (
    <>
      <div className="top">
        <div className="brand">bug<span>fixer</span> platform</div>
        <nav>
          <a href="#/runs" className={r[0] === 'runs' ? 'active' : ''}>Runs</a>
          <a href="#/workflows" className={r[0] === 'workflows' ? 'active' : ''}>Workflows</a>
        </nav>
        <div className="muted" style={{ marginLeft: 'auto', fontSize: 12 }}>toolnexus · postgres · s3</div>
      </div>
      <div className="page">{page}</div>
    </>
  )
}
