import { useEffect, useState } from 'react'
import WorkflowsPage from './pages/WorkflowsPage'
import NewRunPage from './pages/NewRunPage'
import RunPage from './pages/RunPage'
import RunsPage from './pages/RunsPage'
import SkillsPage from './pages/SkillsPage'
import WorkflowBuilderPage from './pages/WorkflowBuilderPage'
import SystemPage from './pages/SystemPage'
import ProjectsPage from './pages/ProjectsPage'
import ProjectPage from './pages/ProjectPage'
import WorkersPage from './pages/WorkersPage'

// tiny hash router. The hierarchy is project → workflow → runs, so the routes
// read that way too:
//   #/projects · #/projects/:name · #/projects/:name/:workflow
//   #/workflows · #/workflows/new · #/workflows/:name/edit · #/workflows/:name/new
//   #/runs · #/runs/:id · #/skills · #/workers · #/system
function useRoute() {
  const [h, setH] = useState(location.hash || '#/projects')
  useEffect(() => { const f = () => setH(location.hash || '#/projects'); addEventListener('hashchange', f); return () => removeEventListener('hashchange', f) }, [])
  return h.replace(/^#/, '').split('/').filter(Boolean)
}

export default function App() {
  const r = useRoute()
  // Projects is the landing page: it is the top of the hierarchy, and a flat
  // list of every run across every repository is not where anyone starts.
  let page = <ProjectsPage />
  if (r[0] === 'projects' && r[1] && r[2]) page = <ProjectPage name={decodeURIComponent(r[1])} workflow={decodeURIComponent(r[2])} />
  else if (r[0] === 'projects' && r[1]) page = <ProjectPage name={decodeURIComponent(r[1])} />
  else if (r[0] === 'projects') page = <ProjectsPage />
  else if (r[0] === 'runs' && !r[1]) page = <RunsPage />
  else if (r[0] === 'workflows' && r[1] === 'new' && !r[2]) page = <WorkflowBuilderPage />
  else if (r[0] === 'workflows' && r[2] === 'edit') page = <WorkflowBuilderPage name={r[1]} />
  else if (r[0] === 'workflows' && r[2] === 'new') page = <NewRunPage name={r[1]} />
  else if (r[0] === 'workflows') page = <WorkflowsPage />
  else if (r[0] === 'runs' && r[1]) page = <RunPage id={r[1]} />
  else if (r[0] === 'skills') page = <SkillsPage />
  else if (r[0] === 'workers') page = <WorkersPage />
  else if (r[0] === 'system') page = <SystemPage />
  return (
    <>
      <div className="top">
        <div className="brand">wf<span>nexus</span></div>
        <nav>
          <a href="#/projects" className={r[0] === 'projects' ? 'active' : ''}>Projects</a>
          <a href="#/runs" className={r[0] === 'runs' ? 'active' : ''}>All runs</a>
          <a href="#/workflows" className={r[0] === 'workflows' && r[1] !== 'new' && r[2] !== 'edit' ? 'active' : ''}>Workflows</a>
          <a href="#/workflows/new" className={r[0] === 'workflows' && (r[1] === 'new' || r[2] === 'edit') ? 'active' : ''}>Builder</a>
          <a href="#/skills" className={r[0] === 'skills' ? 'active' : ''}>Skills &amp; tools</a>
          <a href="#/workers" className={r[0] === 'workers' ? 'active' : ''}>Workers</a>
          <a href="#/system" className={r[0] === 'system' ? 'active' : ''}>System</a>
        </nav>
        <div className="spacer">toolnexus · postgres · s3</div>
      </div>
      <div className="page">{page}</div>
    </>
  )
}
