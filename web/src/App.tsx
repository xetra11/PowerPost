import { NavLink, Route, Routes, useLocation, useNavigate } from 'react-router-dom'
import PostsPage from './pages/PostsPage'
import EditorPage from './pages/EditorPage'
import ReviewPage from './pages/ReviewPage'
import SettingsPage from './pages/SettingsPage'
import { useProjects } from './project'

export default function App() {
  return (
    <>
      <header className="container">
        <nav>
          <ul>
            <li>
              <NavLink to="/" className="brand">
                <img src="/logo-192.png" alt="" width={28} height={28} /> PowerPoster
              </NavLink>
            </li>
            <li>
              <ProjectSwitch />
            </li>
          </ul>
          <ul>
            <li>
              <NavLink to="/" end>
                Posts
              </NavLink>
            </li>
            <li>
              <NavLink to="/settings">Settings</NavLink>
            </li>
          </ul>
        </nav>
      </header>
      <main className="container">
        <Routes>
          <Route path="/" element={<PostsPage />} />
          <Route path="/posts/:id" element={<EditorPage />} />
          <Route path="/posts/:id/review" element={<ReviewPage />} />
          <Route path="/settings" element={<SettingsPage />} />
          <Route path="*" element={<p>Page not found.</p>} />
        </Routes>
      </main>
    </>
  )
}

const newProject = 'new'

function ProjectSwitch() {
  const { projects, current, select } = useProjects()
  const navigate = useNavigate()
  const { pathname } = useLocation()
  if (!projects || !current) return null
  return (
    <select
      className="project-switch"
      aria-label="Project"
      value={current.id}
      onChange={(e) => {
        if (e.target.value === newProject) {
          navigate('/settings#projects')
          return
        }
        select(Number(e.target.value))
        // A post page belongs to the old project; settings follow the switch.
        if (pathname.startsWith('/posts/')) navigate('/')
      }}
    >
      {projects.map((p) => (
        <option key={p.id} value={p.id}>
          {p.name}
        </option>
      ))}
      <option value={newProject}>+ New project…</option>
    </select>
  )
}
