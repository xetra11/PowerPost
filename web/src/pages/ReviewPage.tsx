import { useEffect, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { api, describeMedia, type Attempt, type Platform, type Post, type Target } from '../api'
import { CopyButton, ErrorBanner, IssueList, StatusBadge } from '../components/common'
import { usePolling } from '../usePolling'

const busy = (p: Post | null) =>
  !!p &&
  (p.status === 'publishing' ||
    p.targets.some((t) => t.enabled && (t.status === 'queued' || t.status === 'publishing' || t.rendition?.status === 'pending' || t.rendition?.status === 'processing')))

export default function ReviewPage() {
  const id = Number(useParams().id)
  const { data: post, setData: setPost, error: loadError } = usePolling(() => api.post(id), 1500, busy)
  const [platforms, setPlatforms] = useState<Platform[]>([])
  const [safeZones, setSafeZones] = useState(false)
  const [working, setWorking] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [attempts, setAttempts] = useState<Attempt[]>([])

  useEffect(() => {
    api.platforms().then(setPlatforms).catch((e) => setError(e.message))
  }, [])
  const status = post?.status
  useEffect(() => {
    api.attempts(id).then(setAttempts).catch(() => {})
  }, [id, status])

  if (!post) return loadError ? <ErrorBanner error={loadError} /> : <p aria-busy="true">Loading…</p>

  const act = async (fn: () => Promise<Post>) => {
    setWorking(true)
    setError(null)
    try {
      setPost(await fn())
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setWorking(false)
    }
  }

  const enabled = post.targets.filter((t) => t.enabled)
  const pending = enabled.filter((t) => t.status !== 'published')
  const contentErrors = post.issues.filter((i) => i.severity === 'error' && i.scope === 'content')
  const accountErrors = post.issues.filter((i) => i.severity === 'error' && i.scope === 'account')
  const editable = post.status === 'draft' || post.status === 'approved' || post.status === 'failed'

  const publish = (platforms: string[] = []) => {
    const names = (platforms.length ? enabled.filter((t) => platforms.includes(t.platform)) : pending).map((t) => t.displayName)
    if (!confirm(`Publish now to ${names.join(' and ')}?`)) return
    act(() => api.publish(id, platforms))
  }

  return (
    <>
      <div className="page-head">
        <div>
          <Link to={`/posts/${id}`}>← Back to edit</Link>
          <h2>
            Review: {post.title} <StatusBadge status={post.status} />
          </h2>
        </div>
        <label className="inline">
          <input type="checkbox" role="switch" checked={safeZones} onChange={(e) => setSafeZones(e.target.checked)} />
          Show safe zones
        </label>
      </div>
      <ErrorBanner error={error} onClose={() => setError(null)} />

      <div className="review-grid">
        {enabled.map((t) => (
          <ReviewCard
            key={t.platform}
            post={post}
            target={t}
            platform={platforms.find((p) => p.id === t.platform)}
            safeZones={safeZones}
            onRetry={post.canPublish && t.status === 'failed' ? () => publish([t.platform]) : undefined}
          />
        ))}
        {enabled.length === 0 && <p>No platform selected. Go back and enable at least one.</p>}
      </div>

      <article className="action-bar">
        {post.status === 'draft' || (post.status === 'failed' && !post.approvalValid) ? (
          <>
            <div>
              {post.ready ? (
                <strong>Everything is converted and valid. Approve to unlock publishing.</strong>
              ) : contentErrors.length ? (
                <span>Fix {contentErrors.length} issue(s) before approving.</span>
              ) : (
                <span aria-busy="true">Waiting for conversion…</span>
              )}
            </div>
            <button onClick={() => act(() => api.approve(id))} disabled={!post.ready || working} aria-busy={working}>
              Approve
            </button>
          </>
        ) : post.status === 'publishing' ? (
          <span aria-busy="true">Publishing… you can leave this page; it continues in the background.</span>
        ) : post.status === 'published' ? (
          <strong>Published on all platforms 🎉</strong>
        ) : (
          <>
            <div>
              {post.approvalValid ? <strong>Approved ✓</strong> : <strong>Changed since approval — approve again.</strong>}
              {accountErrors.length > 0 && <IssueList issues={accountErrors} />}
            </div>
            <div className="actions">
              {editable && (
                <button className="secondary outline" onClick={() => act(() => api.unapprove(id))} disabled={working}>
                  Unapprove
                </button>
              )}
              <button onClick={() => publish()} disabled={!post.canPublish || working || pending.length === 0} aria-busy={working}>
                {post.status === 'partial' || post.status === 'failed' ? 'Publish remaining' : 'Publish now'}
              </button>
            </div>
          </>
        )}
      </article>

      {attempts.length > 0 && (
        <details>
          <summary>Publish log ({attempts.length})</summary>
          {attempts.map((a) => {
            const t = post.targets.find((x) => x.id === a.targetId)
            return (
              <div key={a.id} className="attempt">
                <strong>
                  {t?.displayName ?? a.targetId} — {a.status}
                </strong>{' '}
                <small>{new Date(a.startedAt).toLocaleString()}</small>
                {a.error && <p className="error-text">{a.error}</p>}
                <pre>{a.log}</pre>
              </div>
            )
          })}
        </details>
      )}
    </>
  )
}

function ReviewCard({
  post,
  target: t,
  platform,
  safeZones,
  onRetry,
}: {
  post: Post
  target: Target
  platform: Platform | undefined
  safeZones: boolean
  onRetry?: () => void
}) {
  const r = t.rendition
  const options = platform ? Object.entries(platform.optionsSchema.properties) : []
  const inbox = t.options.mode === 'inbox'
  return (
    <article className="review-card">
      <header className="platform-head">
        <strong>{t.displayName}</strong>
        <StatusBadge status={t.status} />
      </header>

      <div className="phone">
        {r?.status === 'ready' ? (
          <video src={r.url} controls playsInline preload="metadata" />
        ) : (
          <div className="phone-placeholder">
            {r?.status === 'failed' ? 'Conversion failed' : r ? `Converting… ${Math.round(r.progress * 100)}%` : post.asset ? 'Waiting…' : 'No video'}
          </div>
        )}
        {safeZones && (
          <div className="safe-zones" aria-hidden>
            <div className="sz-top" />
            <div className="sz-bottom" />
            <div className="sz-right" />
          </div>
        )}
      </div>
      {r?.info && <small className="muted">{describeMedia(r.info)}</small>}

      {(t.status === 'queued' || t.status === 'publishing') && <p aria-busy="true">{t.stage || 'Waiting…'}</p>}
      {t.status === 'published' && (
        <div className="banner banner-ok">
          {t.permalink ? (
            <a href={t.permalink} target="_blank" rel="noreferrer">
              View on {t.displayName} ↗
            </a>
          ) : (
            'Done.'
          )}
          {t.note && <p>{t.note}</p>}
        </div>
      )}
      {t.status === 'failed' && (
        <div className="banner banner-error">
          {t.error}
          {onRetry && (
            <button className="small" onClick={onRetry}>
              Retry
            </button>
          )}
        </div>
      )}

      <h6>
        Caption{' '}
        <small className="muted">
          ({t.captionLength}/{t.captionMax})
        </small>
      </h6>
      <pre className="caption">{t.finalCaption || <em>(empty)</em>}</pre>
      {inbox && <CopyButton text={t.finalCaption} label="Copy caption for the TikTok app" />}

      {options.length > 0 && (
        <dl className="options">
          {options.map(([key, prop]) => {
            const v = t.options[key]
            const shown = typeof v === 'boolean' ? (v ? 'Yes' : 'No') : v ? (prop['x-enumLabels']?.[v as string] ?? String(v)) : '—'
            return (
              <div key={key}>
                <dt>{prop.title ?? key}</dt>
                <dd>{shown}</dd>
              </div>
            )
          })}
        </dl>
      )}

      <IssueList issues={t.issues.filter((i) => i.scope === 'content')} />
      {t.notices.map((n) => (
        <p key={n} className="notice">
          {n}
        </p>
      ))}
    </article>
  )
}
