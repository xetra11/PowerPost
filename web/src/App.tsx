import { NavLink, Route, Routes } from 'react-router-dom'
import PostsPage from './pages/PostsPage'
import EditorPage from './pages/EditorPage'
import ReviewPage from './pages/ReviewPage'
import SettingsPage from './pages/SettingsPage'

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
