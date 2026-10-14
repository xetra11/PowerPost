import { useCallback, useEffect, useRef, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { api, describeImages, describeMedia, isConverting, type Attempt, type Platform, type Post, type Stats, type Target } from '../api'
import { CopyButton, ErrorBanner, IssueList, StatusBadge } from '../components/common'
import { PhonePreview, type PreviewMode } from '../components/PhonePreview'
import { usePolling } from '../usePolling'
import { useFollowProject } from '../project'

const busy = (p: Post | null) =>
  !!p &&
  (p.status === 'publishing' ||
    p.targets.some((t) => t.enabled && (t.status === 'queued' || t.status === 'publishing')) ||
    isConverting(p))

export default function ReviewPage() {
  const id = Number(useParams().id)
  const { data: post, setData: setPost, error: loadError } = usePolling(() => api.post(id), 1500, busy)
  const [platforms, setPlatforms] = useState<Platform[]>([])
  const [mode, setModeState] = useState<PreviewMode>(loadMode)
  const userPickedMode = useRef(false)
  const setMode = (m: PreviewMode) => {
    userPickedMode.current = true
    setModeState(m)
    try {
      localStorage.setItem('pp.previewMode', m)
    } catch {
      /* storage unavailable */
    }
  }
  const [handles, setHandles] = useState<Record<string, string>>({})
  const [working, setWorking] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [attempts, setAttempts] = useState<Attempt[]>([])

  const projectId = post?.projectId
  useFollowProject(projectId)
  useEffect(() => {
    if (!projectId) return
    api
      .platforms(projectId)
      .then((list) => {
        setPlatforms(list)
        // TikTok shows @username; only the live creator info has it.
        for (const p of list.filter((p) => p.connected && p.id === 'tiktok')) {
          api
            .accountInfo(projectId, p.id)
            .then((r) => {
              const u = r.info?.creator_username
              if (typeof u === 'string' && u) setHandles((h) => ({ ...h, [p.id]: u }))
            })
            .catch(() => {})
        }
      })
      .catch((e) => setError(e.message))
  }, [projectId])
  const status = post?.status
  useEffect(() => {
    api.attempts(id).then(setAttempts).catch(() => {})
  }, [id, status])

  // Once per visit: ask the platforms whether published posts still exist.
  const [liveChecked, setLiveChecked] = useState(false)
  const hasPublished = !!post?.targets.some((t) => t.status === 'published')
  useEffect(() => {
    if (!hasPublished || liveChecked) return
    setLiveChecked(true)
    api.checkLive(id).then(setPost).catch(() => {})
  }, [hasPublished, liveChecked, id, setPost])

  // Published posts open in "Live post" mode unless you picked a mode yourself.
  useEffect(() => {
    if (hasPublished && !userPickedMode.current) setModeState('live')
  }, [hasPublished])

  const [stats, setStats] = useState<Record<string, Stats>>({})
  const [statsLoading, setStatsLoading] = useState(false)
  const loadStats = useCallback(() => {
    setStatsLoading(true)
    api
      .stats(id)
      .then(setStats)
      .catch(() => {})
      .finally(() => setStatsLoading(false))
  }, [id])
  useEffect(() => {
    if (hasPublished) loadStats()
  }, [hasPublished, loadStats])

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
        <div role="group" className="mode-switch" aria-label="Preview mode">
          {(
            [
              ['live', 'Live post'],
              ['simulation', 'App preview'],
              ['safezones', 'Safe zones'],
              ['plain', 'Media only'],
            ] as const
          ).map(([m, label]) => (
            <button key={m} className={mode === m ? 'small' : 'small secondary outline'} onClick={() => setMode(m)}>
              {label}
            </button>
          ))}
        </div>
      </div>
      <ErrorBanner error={error} onClose={() => setError(null)} />

      <div className="review-grid">
        {enabled.map((t) => (
          <ReviewCard
            key={t.platform}
            post={post}
            target={t}
            platform={platforms.find((p) => p.id === t.platform)}
            mode={mode}
            handle={handles[t.platform]}
            approved={post.approvalValid}
            onCheckLive={() => act(() => api.checkLive(id))}
            stats={stats[t.platform]}
            statsLoading={statsLoading}
            onRefreshStats={loadStats}
            onPublish={t.canPublish ? () => publish([t.platform]) : undefined}
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
              {post.canPublish && !post.canPublishAll && <p className="muted">Some platforms aren't ready yet — publish the others individually on their cards.</p>}
            </div>
            <div className="actions">
              {editable && (
                <button className="secondary outline" onClick={() => act(() => api.unapprove(id))} disabled={working}>
                  Unapprove
                </button>
              )}
              <button onClick={() => publish()} disabled={!post.canPublishAll || working} aria-busy={working}>
                {pending.length > 1 ? `Publish all (${pending.length})` : post.status === 'partial' || post.status === 'failed' ? 'Publish remaining' : 'Publish now'}
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
  mode,
  handle,
  approved,
  onPublish,
  onCheckLive,
  stats,
  statsLoading,
  onRefreshStats,
}: {
  post: Post
  target: Target
  platform: Platform | undefined
  mode: PreviewMode
  handle?: string
  approved: boolean
  onPublish?: () => void
  onCheckLive: () => void
  stats?: Stats
  statsLoading: boolean
  onRefreshStats: () => void
}) {
  const r = t.rendition
  const images = post.kind === 'image'
  const imagesReady = images && t.renditions.length === post.assets.length && t.renditions.every((x) => x.status === 'ready')
  const options = platform ? Object.entries(platform.optionsSchema.properties) : []
  const inbox = t.options.mode === 'inbox'
  return (
    <article className="review-card">
      <header className="platform-head">
        <strong>{t.displayName}</strong>
        <StatusBadge status={t.status} />
      </header>

      {t.status === 'published' && <StatsBar stats={stats} loading={statsLoading} onRefresh={onRefreshStats} />}
      {mode === 'live' && t.status === 'published' && t.embedUrl ? (
        <iframe
          className={`live-embed live-${t.platform}`}
          src={t.embedUrl}
          title={`${t.displayName} post`}
          loading="lazy"
          allow="autoplay; encrypted-media; picture-in-picture; clipboard-write"
          allowFullScreen
        />
      ) : mode === 'live' && t.status === 'published' ? (
        <div className="phone">
          <div className="phone-placeholder live-unavailable">
            This post can't be embedded (inbox upload or private post).
            {t.permalink && (
              <a href={t.permalink} target="_blank" rel="noreferrer">
                Open on {t.displayName} ↗
              </a>
            )}
          </div>
        </div>
      ) : (
      <PhonePreview
        platform={t.platform}
        videoUrl={!images && r?.status === 'ready' ? r.url : null}
        images={images && imagesReady ? t.renditions.map((x) => x.url) : null}
        placeholder={
          t.renditions.some((x) => x.status === 'failed')
            ? 'Conversion failed'
            : images && t.renditions.length > 0
              ? `Converting… ${t.renditions.filter((x) => x.status === 'ready').length}/${post.assets.length}`
              : r
                ? `Converting… ${Math.round(r.progress * 100)}%`
                : post.asset
                  ? 'Waiting…'
                  : 'No media'
        }
        caption={t.finalCaption}
        username={handle ?? platform?.accountName ?? ''}
        label={disclosureLabel(t)}
        mode={mode === 'live' ? 'simulation' : mode}
      />
      )}
      {images
        ? imagesReady && <small className="muted">{describeImages(t.renditions.flatMap((x) => (x.info ? [x.info] : [])))}</small>
        : r?.info && <small className="muted">{describeMedia(r.info)}</small>}
      {t.platform !== 'discord' && (t.platform === 'instagram' ? post.coverUrl : post.coverFrameUrl) && (
        <div className="cover-mini">
          <img src={t.platform === 'instagram' ? post.coverUrl : post.coverFrameUrl} alt="" />
          <span className="muted">
            Cover: {t.platform === 'instagram' && post.coverCustom ? 'custom image' : `frame at ${(post.coverMs / 1000).toFixed(1)} s`}
          </span>
        </div>
      )}

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
          <button className="small secondary outline" onClick={onCheckLive}>
            Check if still online
          </button>
        </div>
      )}
      {t.status === 'removed' && <div className="banner banner-info">{t.note || 'This post was deleted on the platform.'}</div>}
      {t.status === 'failed' && (
        <div className="banner banner-error">
          {t.error}
        </div>
      )}
      {approved && (t.status === 'idle' || t.status === 'failed' || t.status === 'removed') && (
        <div className="card-publish">
          {onPublish ? (
            <button onClick={onPublish}>{t.status === 'failed' ? `Retry ${t.displayName}` : `Publish to ${t.displayName}`}</button>
          ) : (
            <>
              <small className="muted">Can't publish to {t.displayName} yet:</small>
              <IssueList issues={t.issues.filter((i) => i.severity === 'error')} />
            </>
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

/** The label TikTok puts on commercial posts (shown in the simulated preview). */
function disclosureLabel(t: Target): string | undefined {
  const o = t.options
  if (t.platform !== 'tiktok' || !o.commercial_content) return undefined
  if (o.brand_content) return 'Paid partnership'
  if (o.brand_organic) return 'Promotional content'
  return undefined
}

function loadMode(): PreviewMode {
  try {
    const m = localStorage.getItem('pp.previewMode')
    if (m === 'live' || m === 'simulation' || m === 'safezones' || m === 'plain') return m
  } catch {
    /* storage unavailable */
  }
  return 'simulation'
}

function StatsBar({ stats, loading, onRefresh }: { stats?: Stats; loading: boolean; onRefresh: () => void }) {
  const fmt = (n?: number) => (n === undefined ? '–' : n >= 10000 ? `${(n / 1000).toFixed(1)}K` : n.toLocaleString())
  return (
    <div className="stats-bar">
      <span title="Likes">♥ {fmt(stats?.likes)}</span>
      <span title="Comments">💬 {fmt(stats?.comments)}</span>
      {stats?.views !== undefined && <span title="Views">▶ {fmt(stats.views)}</span>}
      <button type="button" className="small secondary outline" onClick={onRefresh} disabled={loading} aria-busy={loading}>
        Refresh
      </button>
      {stats?.note && <small className="muted stats-note">{stats.note}</small>}
      {!stats && !loading && <small className="muted stats-note">No numbers available for this platform.</small>}
    </div>
  )
}
