import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import {
  api,
  captionLength,
  composeCaption,
  describeMedia,
  parseHashtags,
  uploadCover,
  uploadMedia,
  type AspectStrategy,
  type Platform,
  type Post,
  type Target,
} from '../api'
import { ErrorBanner, IssueList, OptionsForm, StatusBadge, targetSummary } from '../components/common'
import { CoverPicker } from '../components/CoverPicker'
import { usePolling } from '../usePolling'
import { useFollowProject } from '../project'

interface Draft {
  title: string
  caption: string
  hashtags: string
  overrides: Record<string, { caption: string | null; hashtags: string | null }>
}

function draftFrom(p: Post): Draft {
  const overrides: Draft['overrides'] = {}
  for (const t of p.targets) {
    overrides[t.platform] = { caption: t.captionOverride, hashtags: t.hashtagsOverride ? t.hashtagsOverride.join(' ') : null }
  }
  return { title: p.title, caption: p.caption, hashtags: p.hashtags.join(' '), overrides }
}

const isConverting = (p: Post | null) =>
  !!p && p.targets.some((t) => t.enabled && (!t.rendition || t.rendition.status === 'pending' || t.rendition.status === 'processing') && !!p.asset)

export default function EditorPage() {
  const id = Number(useParams().id)
  const navigate = useNavigate()
  const { data: post, setData: setPost, error: loadError } = usePolling(() => api.post(id), 1500, isConverting)
  const [platforms, setPlatforms] = useState<Platform[]>([])
  const [draft, setDraft] = useState<Draft | null>(null)
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const [upload, setUpload] = useState<number | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [dragOver, setDragOver] = useState(false)
  const videoRef = useRef<HTMLVideoElement>(null)

  const projectId = post?.projectId
  useFollowProject(projectId)
  useEffect(() => {
    if (projectId) api.platforms(projectId).then(setPlatforms).catch((e) => setError(e.message))
  }, [projectId])
  useEffect(() => {
    if (post && !dirty) setDraft(draftFrom(post))
  }, [post, dirty])

  if (!post || !draft) return loadError ? <ErrorBanner error={loadError} /> : <p aria-busy="true">Loading…</p>

  const editable = post.status === 'draft' || post.status === 'approved' || post.status === 'failed'
  const platformOf = (t: Target) => platforms.find((p) => p.id === t.platform)

  const edit = (patch: Partial<Draft>) => {
    setDraft({ ...draft, ...patch })
    setDirty(true)
  }
  const editOverride = (platform: string, patch: Partial<Draft['overrides'][string]>) => {
    edit({ overrides: { ...draft.overrides, [platform]: { ...draft.overrides[platform], ...patch } } })
  }

  // Runs an immediate server action and shows the returned post.
  const act = async (fn: () => Promise<Post>) => {
    setError(null)
    try {
      setPost(await fn())
    } catch (e) {
      setError((e as Error).message)
    }
  }

  const save = async (): Promise<boolean> => {
    setSaving(true)
    setError(null)
    try {
      let p = await api.updatePost(id, { title: draft.title, caption: draft.caption, hashtags: parseHashtags(draft.hashtags) })
      for (const t of p.targets) {
        const o = draft.overrides[t.platform]
        if (!o) continue
        const hashtags = o.hashtags === null ? null : parseHashtags(o.hashtags)
        const captionChanged = o.caption !== t.captionOverride
        const tagsChanged = JSON.stringify(hashtags) !== JSON.stringify(t.hashtagsOverride)
        if (!captionChanged && !tagsChanged) continue
        p = await api.updateTarget(id, t.platform, {
          ...(captionChanged ? (o.caption === null ? { useBaseCaption: true } : { captionOverride: o.caption }) : {}),
          ...(tagsChanged ? (hashtags === null ? { useBaseHashtags: true } : { hashtagsOverride: hashtags }) : {}),
        })
      }
      setPost(p)
      setDirty(false)
      return true
    } catch (e) {
      setError((e as Error).message)
      return false
    } finally {
      setSaving(false)
    }
  }

  const onFile = async (file: File | undefined) => {
    if (!file) return
    setError(null)
    setUpload(0)
    try {
      setPost(await uploadMedia(id, file, setUpload))
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setUpload(null)
    }
  }

  const remove = async () => {
    if (!confirm(`Delete "${post.title}" and its video files?`)) return
    try {
      await api.deletePost(id)
      navigate('/')
    } catch (e) {
      setError((e as Error).message)
    }
  }

  const duplicate = async () => {
    try {
      const copy = await api.duplicatePost(id)
      navigate(`/posts/${copy.id}`)
    } catch (e) {
      setError((e as Error).message)
    }
  }

  const review = async () => {
    if (dirty && !(await save())) return
    navigate(`/posts/${id}/review`)
  }

  const baseTags = parseHashtags(draft.hashtags)

  return (
    <>
      <div className="page-head">
        <div>
          <Link to="/">← Posts</Link>
          <h2>
            {post.title} <StatusBadge status={post.status} />
          </h2>
        </div>
        <div className="actions">
          {dirty && (
            <button onClick={save} aria-busy={saving} disabled={saving}>
              Save changes
            </button>
          )}
          <button className={dirty ? 'secondary' : ''} onClick={review}>
            Review &amp; approve →
          </button>
        </div>
      </div>
      <ErrorBanner error={error} onClose={() => setError(null)} />
      {!editable && (
        <div className="banner banner-info">
          This post was {post.status === 'publishing' ? 'sent for publishing' : 'already published'} and can't be edited.{' '}
          {post.status !== 'publishing' && (
            <button type="button" className="small" onClick={duplicate}>
              Duplicate to edit and post again
            </button>
          )}
        </div>
      )}
      {post.status === 'approved' && editable && <div className="banner banner-info">Approved. Any change sends it back to draft.</div>}

      <div className="grid editor-grid">
        <article>
          <header>
            <strong>Video</strong>
          </header>
          {post.asset ? (
            <>
              <video ref={videoRef} src={post.asset.url} poster={post.asset.thumbUrl || undefined} controls preload="metadata" className="source-video" />
              <small className="muted">
                {post.asset.originalName} · {describeMedia(post.asset.info)}
              </small>
            </>
          ) : null}
          {editable && (
            <label
              className={`dropzone ${dragOver ? 'over' : ''}`}
              onDragOver={(e) => {
                e.preventDefault()
                setDragOver(true)
              }}
              onDragLeave={() => setDragOver(false)}
              onDrop={(e) => {
                e.preventDefault()
                setDragOver(false)
                onFile(e.dataTransfer.files[0])
              }}
            >
              {upload !== null ? (
                <>
                  Uploading… {Math.round(upload * 100)}%
                  <progress value={upload} max={1} />
                </>
              ) : (
                <>{post.asset ? 'Replace video' : 'Drop a video here or click to choose'}</>
              )}
              <input type="file" accept="video/*" hidden disabled={upload !== null} onChange={(e) => onFile(e.target.files?.[0])} />
            </label>
          )}

          <label>
            Fit to 9:16
            <select
              value={post.aspectStrategy}
              disabled={!editable}
              onChange={(e) => act(() => api.updatePost(id, { aspectStrategy: e.target.value as AspectStrategy }))}
            >
              <option value="blur">Blurred background (good for gameplay)</option>
              <option value="crop">Crop to fill</option>
              <option value="pad">Black bars</option>
            </select>
            <small className="hint">Used when the video isn't already vertical.</small>
          </label>

          {post.asset && (
            <CoverPicker
              post={post}
              disabled={!editable}
              onPick={(ms) => act(() => api.updatePost(id, { coverMs: ms }))}
              onUpload={(f) => act(() => uploadCover(id, f))}
              onClearImage={() => act(() => api.clearCover(id))}
            />
          )}
        </article>

        <article>
          <header>
            <strong>Text</strong>
          </header>
          <label>
            Title <small className="muted">(internal)</small>
            <input value={draft.title} disabled={!editable} onChange={(e) => edit({ title: e.target.value })} />
          </label>
          <label>
            Caption
            <textarea rows={6} value={draft.caption} disabled={!editable} onChange={(e) => edit({ caption: e.target.value })} />
          </label>
          <label>
            Hashtags
            <input value={draft.hashtags} disabled={!editable} placeholder="indiegame gamedev" onChange={(e) => edit({ hashtags: e.target.value })} />
            <small className="hint">Separate with spaces. They're added to the end of the caption unless already in it.</small>
          </label>
          <div className="counters">
            {post.targets
              .filter((t) => t.enabled)
              .map((t) => {
                const p = platformOf(t)
                if (!p) return null
                const o = draft.overrides[t.platform]
                const text = composeCaption(o?.caption ?? draft.caption, o?.hashtags != null ? parseHashtags(o.hashtags) : baseTags)
                const n = captionLength(text, p.constraints.caption.countUtf16)
                const max = p.constraints.caption.maxChars
                return (
                  <span key={t.platform} className={n > max ? 'over' : ''}>
                    {t.displayName}: {n}/{max}
                  </span>
                )
              })}
          </div>
        </article>
      </div>

      <h3>Platforms</h3>
      {post.targets.map((t) => {
        const p = platformOf(t)
        const s = targetSummary(t)
        const o = draft.overrides[t.platform] ?? { caption: null, hashtags: null }
        return (
          <article key={t.platform} className={`platform-card ${t.enabled ? '' : 'disabled'}`}>
            <header className="platform-head">
              <label>
                <input
                  type="checkbox"
                  role="switch"
                  checked={t.enabled}
                  disabled={!editable}
                  onChange={(e) => act(() => api.updateTarget(id, t.platform, { enabled: e.target.checked }))}
                />
                <strong>{t.displayName}</strong>
              </label>
              {t.enabled && <span className={`chip tone-${s.tone}`}>{s.icon} {s.text}</span>}
            </header>
            {t.enabled && (
              <div className="grid platform-body">
                <div>
                  <AccountLine projectId={post.projectId} platform={p} />
                  <label>
                    <input
                      type="checkbox"
                      disabled={!editable}
                      checked={o.caption !== null}
                      onChange={(e) => editOverride(t.platform, { caption: e.target.checked ? draft.caption : null })}
                    />
                    Different caption for {t.displayName}
                  </label>
                  {o.caption !== null && (
                    <textarea rows={4} value={o.caption} disabled={!editable} onChange={(e) => editOverride(t.platform, { caption: e.target.value })} />
                  )}
                  <label>
                    <input
                      type="checkbox"
                      disabled={!editable}
                      checked={o.hashtags !== null}
                      onChange={(e) => editOverride(t.platform, { hashtags: e.target.checked ? draft.hashtags : null })}
                    />
                    Different hashtags for {t.displayName}
                  </label>
                  {o.hashtags !== null && (
                    <input value={o.hashtags} disabled={!editable} onChange={(e) => editOverride(t.platform, { hashtags: e.target.value })} />
                  )}
                  {p && (
                    <OptionsForm
                      schema={p.optionsSchema}
                      values={t.options}
                      disabled={!editable}
                      onChange={(key, value) => act(() => api.updateTarget(id, t.platform, { options: { [key]: value } }))}
                    />
                  )}
                </div>
                <div>
                  <h6>Checks</h6>
                  {t.rendition?.status === 'processing' && <progress value={t.rendition.progress} max={1} />}
                  <IssueList issues={t.issues} />
                  {t.issues.length === 0 && <p className="muted">All good.</p>}
                </div>
              </div>
            )}
          </article>
        )
      })}

      <div className="footer-actions">
        <div className="actions">
          <button className="secondary outline" onClick={remove} disabled={post.status === 'publishing'}>
            Delete post
          </button>
          <button className="secondary outline" onClick={duplicate}>
            Duplicate
          </button>
        </div>
        <button onClick={review}>Review &amp; approve →</button>
      </div>
    </>
  )
}

/** Shows which account a post goes to; TikTok requires showing the creator nickname. */
function AccountLine({ projectId, platform }: { projectId: number; platform: Platform | undefined }) {
  const [info, setInfo] = useState<Record<string, unknown> | null>(null)
  useEffect(() => {
    if (platform?.connected && platform.id === 'tiktok') {
      api.accountInfo(projectId, platform.id).then((r) => setInfo(r.info)).catch(() => setInfo(null))
    }
  }, [projectId, platform?.id, platform?.connected])
  if (!platform) return null
  if (!platform.configured)
    return <p className="muted">{platform.displayName} app credentials are missing in .env — see the README to publish here.</p>
  if (!platform.connected)
    return (
      <p className="muted">
        Not connected — <Link to="/settings">connect {platform.displayName}</Link> to publish.
      </p>
    )
  const nick = (info?.creator_nickname as string) || platform.accountName
  const user = info?.creator_username as string | undefined
  return (
    <p className="muted">
      Posting as <strong>{nick}</strong>
      {user ? ` (@${user})` : ''}
    </p>
  )
}
