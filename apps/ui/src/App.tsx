import { useEffect, useState } from 'react'
import { api } from './api'
import NewRunPage from './pages/NewRunPage'
import RunPage from './pages/RunPage'
import SkillsPage from './pages/SkillsPage'
import WorkflowBuilderPage from './pages/WorkflowBuilderPage'
import SystemPage from './pages/SystemPage'
import ProjectsPage from './pages/ProjectsPage'
import ProjectPage from './pages/ProjectPage'
import WorkflowPage from './pages/WorkflowPage'
import WorkersPage from './pages/WorkersPage'
import TemplatesPage from './pages/TemplatesPage'
import Identity from './components/Identity'
import WaitingOnYou from './components/WaitingOnYou'

// tiny hash router. The app is one drill-down — projects → a project → a
// workflow → a run — and the addresses read the same way:
//   #/projects · #/projects/:p · #/projects/:p/+new
//   #/projects/:p/:w · #/projects/:p/:w/edit · #/projects/:p/:w/run
//   #/projects/:p/:w/runs/:id
//   #/templates · #/skills · #/workers · #/system
// There is no top-level builder and no flat list of every run or every
// workflow: a workflow is made, edited and run from inside its project. The
// older addresses (#/runs/:id, #/workflows/:name/edit|new, #/workflows/new)
// still resolve, so a link somebody saved keeps working.
function useRoute() {
  const [h, setH] = useState(location.hash || '#/projects')
  useEffect(() => { const f = () => setH(location.hash || '#/projects'); addEventListener('hashchange', f); return () => removeEventListener('hashchange', f) }, [])
  return h.replace(/^#/, '').split('/').filter(Boolean)
}

export default function App() {
  const r = useRoute()
  // What this server actually runs on, asked once. It used to be hardcoded as
  // "postgres · s3", which stopped being true when sqlite and folder artifacts
  // arrived — a header that states the backend has to read it, not assume it.
  const [backend, setBackend] = useState('')
  useEffect(() => { api.doctor().then(d => setBackend(`${d.storage.driver} · ${d.storage.artifacts}`)).catch(() => setBackend('')) }, [])
  const d = (x: string) => decodeURIComponent(x)
  let page = <ProjectsPage />
  if (r[0] === 'projects' && r[1]) {
    const p = d(r[1])
    if (r[2] === '+new') page = <WorkflowBuilderPage project={p} />
    else if (r[2] === '+template') page = <TemplatesPage project={p} />
    else if (r[2] && r[3] === 'edit') page = <WorkflowBuilderPage project={p} name={d(r[2])} />
    else if (r[2] && r[3] === 'run') page = <NewRunPage project={p} name={d(r[2])} />
    else if (r[2] && r[3] === 'runs' && r[4]) page = <RunPage id={r[4]} />
    else if (r[2]) page = <WorkflowPage project={p} name={d(r[2])} />
    else page = <ProjectPage name={p} />
  }
  else if (r[0] === 'runs' && r[1]) page = <RunPage id={r[1]} />
  else if (r[0] === 'workflows' && r[1] === 'new' && !r[2]) page = <WorkflowBuilderPage project="local" />
  else if (r[0] === 'workflows' && r[1] && r[2] === 'edit') page = <WorkflowBuilderPage name={d(r[1])} />
  else if (r[0] === 'workflows' && r[1] && r[2] === 'new') page = <NewRunPage name={d(r[1])} />
  else if (r[0] === 'skills') page = <SkillsPage />
  else if (r[0] === 'templates') page = <TemplatesPage />
  else if (r[0] === 'workers') page = <WorkersPage />
  else if (r[0] === 'system') page = <SystemPage />
  const inProjects = !r[0] || ['projects', 'runs', 'workflows'].includes(r[0])
  return (
    <>
      <div className="top">
        <div className="brand">wf<span>nexus</span></div>
        <nav>
          <a href="#/projects" className={inProjects ? 'active' : ''}>Projects</a>
          <a href="#/templates" className={r[0] === 'templates' ? 'active' : ''}>Templates</a>
          <a href="#/skills" className={r[0] === 'skills' ? 'active' : ''}>Skills &amp; tools</a>
          <a href="#/workers" className={r[0] === 'workers' ? 'active' : ''}>Workers</a>
          <a href="#/system" className={r[0] === 'system' ? 'active' : ''}>System</a>
        </nav>
        <div className="spacer" style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
          <span>{backend ? `toolnexus · ${backend}` : 'toolnexus'}</span>
          <WaitingOnYou />
          {/* Nothing at all on a loopback install with no users — see Identity. */}
          <Identity />
        </div>
      </div>
      <div className="page">{page}</div>
    </>
  )
}
