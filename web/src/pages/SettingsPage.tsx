import { useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { api, parseHashtags, type AspectStrategy, type Platform, type Project, type ProjectSettings, type Settings, type SystemStatus } from '../api'
import { CopyButton, ErrorBanner } from '../components/common'
import { useProjects } from '../project'

export default function SettingsPage() {
  const [params, setParams] = useSearchParams()
  const { projects, current, select, reload } = useProjects()
  const [status, setStatus] = useState<SystemStatus | null>(null)
  const [settings, setSettings] = useState<Settings | null>(null)
  const [error, setError] = useState<string | null>(params.get('connect_error'))
  const connected = params.get('connected')

  // Back from a platform sign-in: show the project the account was connected to.
  const fromProject = Number(params.get('project'))
  useEffect(() => {
    if (fromProject > 0) select(fromProject)
  }, [fromProject, select])

  useEffect(() => {
    reload() // fresh post counts
    api.status().then(setStatus).catch((e) => setError(e.message))
    api.settings().then(setSettings).catch((e) => setError(e.message))
  }, [reload])

  const toggleMcpPublish = async (on: boolean) => {
    setError(null)
    try {
      setSettings(await api.saveSettings({ ...settings, mcpAllowPublish: on }))
    } catch (e) {
      setError((e as Error).message)
    }
  }

  const mcpCommand = status ? `claude mcp add --transport http powerposter ${status.mcpUrl}` : ''

  return (
    <>
      <h2>Settings</h2>
      <ErrorBanner
        error={error}
        onClose={() => {
          setError(null)
          setParams({})
        }}
      />
      {connected && <div className="banner banner-ok">Connected {connected}.</div>}

      <ProjectList projects={projects} current={current} onSelect={select} onChanged={reload} onError={setError} />

      {current && <ProjectSettingsForm key={current.id} project={current} onSaved={reload} onError={setError} />}

      <h3>All projects</h3>
      <article>
        <header>
          <strong>Claude (MCP)</strong>
        </header>
        <p>Connect Claude Code to PowerPoster:</p>
        <div className="code-row">
          <code>{mcpCommand}</code>
          <CopyButton text={mcpCommand} />
        </div>
        <p>
          Claude attaches videos from the inbox folder: <code>{status?.inboxHostPath}</code>
        </p>
        <p className="muted">Claude sees all projects and picks one per post by its short name.</p>
        {settings && (
          <label>
            <input type="checkbox" role="switch" checked={settings.mcpAllowPublish} onChange={(e) => toggleMcpPublish(e.target.checked)} />
            Allow Claude to publish posts I have approved
            <small className="hint">Off: Claude can only prepare drafts; you publish from the review page.</small>
          </label>
        )}
      </article>

      {status && (
        <article>
          <header>
            <strong>System</strong>
          </header>
          <ul className="plain">
            <li>
              FFmpeg: {status.ffmpeg === 'ok' ? '✓ available' : <span className="error-text">{status.ffmpeg}</span>} (encoder <code>{status.encoder}</code>)
            </li>
            <li>Public URL (tunnel): {status.publicBaseUrl ? <code>{status.publicBaseUrl}</code> : <span className="muted">not set</span>}</li>
          </ul>
        </article>
      )}
    </>
  )
}

function ProjectList({
  projects,
  current,
  onSelect,
  onChanged,
  onError,
}: {
  projects: Project[] | null
  current: Project | null
  onSelect: (id: number) => void
  onChanged: () => Promise<Project[]>
  onError: (msg: string) => void
}) {
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)

  const create = async () => {
    setBusy(true)
    try {
      const p = await api.createProject(name)
      setName('')
      await onChanged()
      onSelect(p.id)
    } catch (e) {
      onError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  const remove = async (p: Project) => {
    if (!confirm(`Delete the project “${p.name}” and its account connections?`)) return
    try {
      await api.deleteProject(p.id)
      await onChanged()
    } catch (e) {
      onError((e as Error).message)
    }
  }

  return (
    <article id="projects">
      <header>
        <strong>Projects</strong> <small className="muted">— each game or brand has its own brand profile, defaults and accounts</small>
      </header>
      {projects === null ? (
        <p aria-busy="true">Loading…</p>
      ) : (
        <div className="overflow-auto">
          <table className="project-list">
            <tbody>
              {projects.map((p) => (
                <tr key={p.id}>
                  <td className={p.id === current?.id ? 'current' : undefined}>{p.name}</td>
                  <td>
                    <code>{p.slug}</code>
                  </td>
                  <td>
                    <small className="muted">
                      {p.postCount} {p.postCount === 1 ? 'post' : 'posts'}
                    </small>
                  </td>
                  <td className="right">
                    <div className="actions">
                      {p.id === current?.id ? (
                        <small className="muted">Selected</small>
                      ) : (
                        <button className="small secondary" onClick={() => onSelect(p.id)}>
                          Select
                        </button>
                      )}
                      {projects.length > 1 && (
                        <button
                          className="small secondary outline"
                          onClick={() => remove(p)}
                          disabled={p.postCount > 0}
                          title={p.postCount > 0 ? 'Delete its posts first' : undefined}
                        >
                          Delete
                        </button>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <form
        className="code-row"
        onSubmit={(e) => {
          e.preventDefault()
          create()
        }}
      >
        <input placeholder="New project, e.g. the name of your next game" value={name} onChange={(e) => setName(e.target.value)} aria-label="New project name" />
        <button type="submit" className="small" disabled={!name.trim() || busy} aria-busy={busy}>
          Add project
        </button>
      </form>
    </article>
  )
}

function ProjectSettingsForm({ project, onSaved, onError }: { project: Project; onSaved: () => Promise<Project[]>; onError: (msg: string) => void }) {
  const [platforms, setPlatforms] = useState<Platform[] | null>(null)
  const [name, setName] = useState(project.name)
  const [slug, setSlug] = useState(project.slug)
  const [settings, setSettings] = useState<ProjectSettings>(project.settings)
  const [hashtags, setHashtags] = useState(project.settings.brand.defaultHashtags.join(' '))
  const [saved, setSaved] = useState(false)
  const [saving, setSaving] = useState(false)

  const loadPlatforms = () => {
    api.platforms(project.id).then(setPlatforms).catch((e) => onError(e.message))
  }
  useEffect(loadPlatforms, [project.id])

  const connect = async (id: string) => {
    try {
      const { url } = await api.startConnect(project.id, id)
      window.location.href = url
    } catch (e) {
      onError((e as Error).message)
    }
  }
  const disconnect = async (id: string) => {
    if (!confirm(`Disconnect this account from ${project.name}? Publishing to it stops until you connect again.`)) return
    await api.disconnect(project.id, id).catch((e) => onError(e.message))
    loadPlatforms()
  }
  const save = async () => {
    setSaving(true)
    try {
      const p = await api.updateProject(project.id, {
        name,
        slug,
        settings: { ...settings, brand: { ...settings.brand, defaultHashtags: parseHashtags(hashtags) } },
      })
      setName(p.name)
      setSlug(p.slug)
      setSettings(p.settings)
      setHashtags(p.settings.brand.defaultHashtags.join(' '))
      await onSaved()
      setSaved(true)
      setTimeout(() => setSaved(false), 2000)
    } catch (e) {
      onError((e as Error).message)
    } finally {
      setSaving(false)
    }
  }
  const brand = settings.brand
  const setBrand = (patch: Partial<ProjectSettings['brand']>) => setSettings({ ...settings, brand: { ...brand, ...patch } })

  return (
    <>
      <h3>Project: {project.name}</h3>
      <article>
        <header>
          <strong>Connections</strong> <small className="muted">— the accounts {project.name} posts to</small>
        </header>
        {platforms === null ? (
          <p aria-busy="true">Loading…</p>
        ) : (
          <div className="overflow-auto">
            <table>
              <tbody>
                {platforms.map((p) => (
                  <tr key={p.id}>
                    <td>
                      <strong>{p.displayName}</strong>
                    </td>
                    <td>
                      {p.connected ? (
                        <>
                          Connected as <strong>{p.accountName || 'unknown'}</strong>
                          {p.expiresAt && <small className="muted"> · reconnect by {new Date(p.expiresAt).toLocaleDateString()}</small>}
                        </>
                      ) : (
                        <span className="muted">Not connected</span>
                      )}
                      {p.missingSettings.length > 0 && (
                        <small className="hint">
                          To connect, add to <code>.env</code> and restart: {p.missingSettings.map((m, i) => (
                            <span key={m}>
                              {i > 0 && ', '}
                              <code>{m}</code>
                            </span>
                          ))}
                        </small>
                      )}
                      {p.redirectUri && (
                        <small className="hint">
                          Redirect URI to register: <code>{p.redirectUri}</code>
                        </small>
                      )}
                      {p.canImportToken && !p.connected && <TokenForm projectId={project.id} platform={p} onDone={loadPlatforms} onError={onError} />}
                    </td>
                    <td className="right">
                      <div className="actions">
                        <button
                          className="small"
                          onClick={() => connect(p.id)}
                          disabled={p.missingSettings.length > 0}
                          title={p.missingSettings.length > 0 ? `Missing in .env: ${p.missingSettings.join(', ')}` : undefined}
                        >
                          {p.connected ? 'Reconnect' : 'Connect'}
                        </button>
                        {p.connected && (
                          <button className="small secondary outline" onClick={() => disconnect(p.id)}>
                            Disconnect
                          </button>
                        )}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <small className="muted">
          Before connecting another project's account, sign out of the previous account on the platform's website (or use a private window), otherwise the
          platform signs you in with the same account again. Setup steps are in the README (“Connecting platforms”).
        </small>
      </article>

      <form
        onSubmit={(e) => {
          e.preventDefault()
          save()
        }}
      >
        <article>
          <header>
            <strong>Project</strong>
          </header>
          <div className="grid">
            <label>
              Name
              <input value={name} onChange={(e) => setName(e.target.value)} required />
            </label>
            <label>
              Short name
              <input value={slug} onChange={(e) => setSlug(e.target.value)} required />
              <small className="hint">Claude uses it to pick the project, e.g. “a reel for {slug}”.</small>
            </label>
          </div>
        </article>

        <article>
          <header>
            <strong>Brand profile</strong> <small className="muted">— Claude reads this when writing captions</small>
          </header>
          <div className="grid">
            <label>
              Game name
              <input value={brand.gameName} onChange={(e) => setBrand({ gameName: e.target.value })} />
            </label>
            <label>
              Link (store page, website)
              <input value={brand.link} onChange={(e) => setBrand({ link: e.target.value })} />
            </label>
          </div>
          <label>
            Default hashtags
            <input value={hashtags} placeholder="indiegame gamedev" onChange={(e) => setHashtags(e.target.value)} />
          </label>
          <label>
            Tone notes
            <textarea rows={3} value={brand.toneNotes} onChange={(e) => setBrand({ toneNotes: e.target.value })} />
          </label>
        </article>

        <article>
          <header>
            <strong>Defaults for new posts</strong>
          </header>
          <fieldset>
            <legend>Platforms</legend>
            {(platforms ?? []).map((p) => (
              <label key={p.id}>
                <input
                  type="checkbox"
                  checked={settings.defaultPlatforms.includes(p.id)}
                  onChange={(e) =>
                    setSettings({
                      ...settings,
                      defaultPlatforms: e.target.checked ? [...settings.defaultPlatforms, p.id] : settings.defaultPlatforms.filter((x) => x !== p.id),
                    })
                  }
                />
                {p.displayName}
              </label>
            ))}
          </fieldset>
          <label>
            Fit to 9:16
            <select value={settings.defaultAspect} onChange={(e) => setSettings({ ...settings, defaultAspect: e.target.value as AspectStrategy })}>
              <option value="blur">Blurred background</option>
              <option value="crop">Crop to fill</option>
              <option value="pad">Black bars</option>
            </select>
          </label>
        </article>

        <div className="footer-actions">
          {saved && <span className="saved">Saved ✓</span>}
          <button type="submit" aria-busy={saving} disabled={saving}>
            Save project
          </button>
        </div>
      </form>
    </>
  )
}

/** Connect with a token generated in the platform's developer dashboard (no tunnel needed). */
function TokenForm({ projectId, platform, onDone, onError }: { projectId: number; platform: Platform; onDone: () => void; onError: (msg: string) => void }) {
  const [token, setToken] = useState('')
  const [busy, setBusy] = useState(false)
  const submit = async () => {
    setBusy(true)
    try {
      await api.importToken(projectId, platform.id, token)
      setToken('')
      onDone()
    } catch (e) {
      onError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  return (
    <details className="token-form">
      <summary>Or paste an access token</summary>
      <small className="hint">
        Meta dashboard → your app → Instagram → API setup with Instagram login → <em>Generate access tokens</em> → add your account → Generate. No tunnel needed.
      </small>
      <div className="code-row">
        <input type="password" placeholder="Access token" value={token} onChange={(e) => setToken(e.target.value)} aria-label={`${platform.displayName} access token`} />
        <button className="small" onClick={submit} disabled={!token.trim() || busy} aria-busy={busy}>
          Save token
        </button>
      </div>
    </details>
  )
}
