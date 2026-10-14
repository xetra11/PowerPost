import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { api, type Post, type Project } from '../api'
import { ErrorBanner, StatusBadge, targetSummary } from '../components/common'
import { usePolling } from '../usePolling'
import { useProjects } from '../project'

const tabs = [
  { id: 'all', label: 'All', match: () => true },
  { id: 'draft', label: 'Drafts', match: (p: Post) => p.status === 'draft' },
  { id: 'approved', label: 'Approved', match: (p: Post) => p.status === 'approved' || p.status === 'publishing' },
  { id: 'published', label: 'Published', match: (p: Post) => p.status === 'published' || p.status === 'partial' },
  { id: 'failed', label: 'Failed', match: (p: Post) => p.status === 'failed' || p.status === 'partial' },
]

export default function PostsPage() {
  const { current, error } = useProjects()
  if (!current) return error ? <ErrorBanner error={error} /> : <p aria-busy="true">Loading…</p>
  // Keyed so switching projects starts with a fresh list.
  return <PostList key={current.id} project={current} />
}

function PostList({ project }: { project: Project }) {
  const navigate = useNavigate()
  const [tab, setTab] = useState('all')
  const [platform, setPlatform] = useState('')
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const { data: posts, error: loadError } = usePolling(() => api.posts(project.id), 3000, (d) =>
    (d ?? []).some((p) => p.status === 'publishing' || p.targets.some((t) => t.renditions.some((r) => r.status === 'processing'))),
  )

  const create = async () => {
    setCreating(true)
    try {
      const p = await api.createPost(project.id)
      navigate(`/posts/${p.id}`)
    } catch (e) {
      setError((e as Error).message)
      setCreating(false)
    }
  }

  const platforms = [...new Map((posts ?? []).flatMap((p) => p.targets.map((t) => [t.platform, t.displayName] as const))).entries()]
  const current = tabs.find((t) => t.id === tab)!
  const shown = (posts ?? []).filter((p) => current.match(p) && (!platform || p.targets.some((t) => t.platform === platform && t.enabled)))

  return (
    <>
      <div className="page-head">
        <h2>
          Posts <small className="muted">· {project.name}</small>
        </h2>
        <button onClick={create} aria-busy={creating} disabled={creating}>
          New post
        </button>
      </div>
      <ErrorBanner error={error ?? loadError} onClose={() => setError(null)} />

      <div className="toolbar">
        <div role="group" className="tabs">
          {tabs.map((t) => (
            <button key={t.id} className={t.id === tab ? '' : 'secondary outline'} onClick={() => setTab(t.id)}>
              {t.label}
            </button>
          ))}
        </div>
        <select value={platform} onChange={(e) => setPlatform(e.target.value)} aria-label="Filter by platform">
          <option value="">All platforms</option>
          {platforms.map(([id, name]) => (
            <option key={id} value={id}>
              {name}
            </option>
          ))}
        </select>
      </div>

      {posts === null ? (
        <p aria-busy="true">Loading…</p>
      ) : shown.length === 0 ? (
        <article className="empty">
          {posts.length === 0 ? (
            <>
              <p>
                <strong>No posts yet.</strong>
              </p>
              <p>
                Create one with <em>New post</em>, or let Claude prepare one — see <Link to="/settings">Settings → Claude (MCP)</Link>.
              </p>
            </>
          ) : (
            <p>No posts match this filter.</p>
          )}
        </article>
      ) : (
        <div className="overflow-auto">
          <table className="striped">
            <thead>
              <tr>
                <th></th>
                <th>Title</th>
                <th>Status</th>
                <th>Platforms</th>
                <th>Updated</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {shown.map((p) => (
                <tr key={p.id}>
                  <td className="thumb-cell">
                    {p.asset?.thumbUrl ? <img src={p.asset.thumbUrl} alt="" className="thumb" /> : <div className="thumb empty-thumb" />}
                    {p.assets.length > 1 && <span className="thumb-count" title={`${p.assets.length} images`}>{p.assets.length}</span>}
                  </td>
                  <td>
                    <Link to={`/posts/${p.id}`}>{p.title}</Link>
                    {p.createdBy === 'mcp' && <small className="by-claude">by Claude</small>}
                  </td>
                  <td>
                    <StatusBadge status={p.status} />
                  </td>
                  <td>
                    {p.targets
                      .filter((t) => t.enabled)
                      .map((t) => {
                        const s = targetSummary(t)
                        return (
                          <span key={t.platform} className={`chip tone-${s.tone}`} title={s.text}>
                            {s.icon} {t.displayName}
                          </span>
                        )
                      })}
                  </td>
                  <td>
                    <small>{new Date(p.updatedAt).toLocaleString()}</small>
                  </td>
                  <td>
                    <Link to={`/posts/${p.id}/review`}>Review</Link>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  )
}
