import { useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { api, parseHashtags, type AspectStrategy, type Platform, type Settings, type SystemStatus } from '../api'
import { CopyButton, ErrorBanner } from '../components/common'

export default function SettingsPage() {
  const [params, setParams] = useSearchParams()
  const [platforms, setPlatforms] = useState<Platform[] | null>(null)
  const [settings, setSettings] = useState<Settings | null>(null)
  const [hashtags, setHashtags] = useState('')
  const [status, setStatus] = useState<SystemStatus | null>(null)
  const [error, setError] = useState<string | null>(params.get('connect_error'))
  const [saved, setSaved] = useState(false)
  const [saving, setSaving] = useState(false)
  const connected = params.get('connected')

  const load = () => {
    api.platforms().then(setPlatforms).catch((e) => setError(e.message))
    api.status().then(setStatus).catch((e) => setError(e.message))
  }
  useEffect(() => {
    load()
    api
      .settings()
      .then((s) => {
        setSettings(s)
        setHashtags(s.brand.defaultHashtags.join(' '))
      })
      .catch((e) => setError(e.message))
  }, [])

  const connect = async (id: string) => {
    setError(null)
    try {
      const { url } = await api.startConnect(id)
      window.location.href = url
    } catch (e) {
      setError((e as Error).message)
    }
  }
  const disconnect = async (id: string) => {
    if (!confirm('Disconnect this account? Publishing to it stops until you connect again.')) return
    await api.disconnect(id).catch((e) => setError(e.message))
    load()
  }
  const save = async (next: Settings) => {
    setSaving(true)
    setError(null)
    try {
      const s = await api.saveSettings({ ...next, brand: { ...next.brand, defaultHashtags: parseHashtags(hashtags) } })
      setSettings(s)
      setHashtags(s.brand.defaultHashtags.join(' '))
      setSaved(true)
      setTimeout(() => setSaved(false), 2000)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setSaving(false)
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

      <article>
        <header>
          <strong>Connections</strong>
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
                      {p.canImportToken && !p.connected && <TokenForm platform={p} onDone={load} onError={setError} />}
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
        <small className="muted">Setup steps for each platform are in the README (“Connecting platforms”).</small>
      </article>

      {settings && (
        <form
          onSubmit={(e) => {
            e.preventDefault()
            save(settings)
          }}
        >
          <article>
            <header>
              <strong>Brand profile</strong> <small className="muted">— Claude reads this when writing captions</small>
            </header>
            <div className="grid">
              <label>
                Game name
                <input value={settings.brand.gameName} onChange={(e) => setSettings({ ...settings, brand: { ...settings.brand, gameName: e.target.value } })} />
              </label>
              <label>
                Link (store page, website)
                <input value={settings.brand.link} onChange={(e) => setSettings({ ...settings, brand: { ...settings.brand, link: e.target.value } })} />
              </label>
            </div>
            <label>
              Default hashtags
              <input value={hashtags} placeholder="indiegame gamedev" onChange={(e) => setHashtags(e.target.value)} />
            </label>
            <label>
              Tone notes
              <textarea rows={3} value={settings.brand.toneNotes} onChange={(e) => setSettings({ ...settings, brand: { ...settings.brand, toneNotes: e.target.value } })} />
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
            <label>
              <input
                type="checkbox"
                role="switch"
                checked={settings.mcpAllowPublish}
                onChange={(e) => setSettings({ ...settings, mcpAllowPublish: e.target.checked })}
              />
              Allow Claude to publish posts I have approved
              <small className="hint">Off: Claude can only prepare drafts; you publish from the review page.</small>
            </label>
          </article>

          <div className="footer-actions">
            {saved && <span className="saved">Saved ✓</span>}
            <button type="submit" aria-busy={saving} disabled={saving}>
              Save settings
            </button>
          </div>
        </form>
      )}

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

/** Connect with a token generated in the platform's developer dashboard (no tunnel needed). */
function TokenForm({ platform, onDone, onError }: { platform: Platform; onDone: () => void; onError: (msg: string) => void }) {
  const [token, setToken] = useState('')
  const [busy, setBusy] = useState(false)
  const submit = async () => {
    setBusy(true)
    try {
      await api.importToken(platform.id, token)
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
