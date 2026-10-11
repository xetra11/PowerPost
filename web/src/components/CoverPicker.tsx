import { useEffect, useRef, useState } from 'react'
import type { Post } from '../api'

/**
 * Scrub through the converted video and pick the cover frame. Shows exactly the
 * frame Instagram/TikTok get (after reframing to 9:16). Falls back to the
 * original video while the conversion is still running.
 */
export function CoverPicker({
  post,
  disabled,
  onPick,
  onUpload,
  onClearImage,
}: {
  post: Post
  disabled: boolean
  onPick: (ms: number) => void
  onUpload: (file: File) => void
  onClearImage: () => void
}) {
  const rendition = post.targets.find((t) => t.enabled && t.rendition?.status === 'ready')?.rendition
  const src = rendition?.url || post.asset?.url || ''
  const info = rendition?.info ?? post.asset?.info
  const duration = info?.duration ?? 0
  const step = info?.fps ? Math.max(1, Math.round(1000 / info.fps)) : 40 // one frame

  const videoRef = useRef<HTMLVideoElement>(null)
  const [ms, setMs] = useState(post.coverMs)
  const [startAt] = useState(post.coverMs / 1000) // initial frame; later seeks go through currentTime
  const target = useRef(post.coverMs)

  // Follow the saved value when it changes from elsewhere (e.g. Claude).
  useEffect(() => setMs(post.coverMs), [post.coverMs])

  // Seek the preview to the slider position. While a seek is running, only the
  // latest position is remembered and applied when it finishes (see onSeeked).
  useEffect(() => {
    target.current = ms
    const v = videoRef.current
    if (v && !v.seeking) v.currentTime = ms / 1000
  }, [ms])
  const onSeeked = () => {
    const v = videoRef.current
    if (v && Math.abs(v.currentTime * 1000 - target.current) > step / 2) v.currentTime = target.current / 1000
  }

  if (!src) return null
  const maxMs = Math.max(0, Math.floor(duration * 1000) - step)
  const clamp = (v: number) => Math.min(maxMs, Math.max(0, v))
  const dirty = ms !== post.coverMs

  return (
    <div className="cover-picker">
      <div className="cover-stage">
        <video ref={videoRef} src={src + '#t=' + startAt} muted playsInline preload="auto" className="cover-video" onSeeked={onSeeked} onLoadedData={onSeeked} />
        {post.coverCustom && post.coverUrl && (
          <img src={post.coverUrl} alt="Custom cover" className="cover-custom-badge" title="Custom image used on Instagram" />
        )}
      </div>
      <div className="cover-controls">
        <strong>Cover</strong>
        <input
          type="range"
          min={0}
          max={maxMs}
          step={step}
          value={clamp(ms)}
          disabled={disabled || maxMs === 0}
          onChange={(e) => setMs(clamp(Number(e.target.value)))}
          aria-label="Cover frame position"
        />
        <div className="cover-row">
          <button type="button" className="secondary outline small" disabled={disabled} onClick={() => setMs(clamp(ms - step))} aria-label="Previous frame">
            ◀
          </button>
          <span className="cover-time">
            {(ms / 1000).toFixed(2)} s <span className="muted">/ {duration.toFixed(1)} s</span>
          </span>
          <button type="button" className="secondary outline small" disabled={disabled} onClick={() => setMs(clamp(ms + step))} aria-label="Next frame">
            ▶
          </button>
        </div>
        <div className="actions">
          <button type="button" className="small" disabled={disabled || !dirty} onClick={() => onPick(clamp(ms))}>
            {dirty ? 'Use this frame' : 'Current cover ✓'}
          </button>
          {dirty && (
            <button type="button" className="secondary outline small" onClick={() => setMs(post.coverMs)}>
              Reset
            </button>
          )}
        </div>
        <small className="muted">
          {post.coverCustom
            ? `Instagram uses your uploaded image; TikTok uses this frame (${(post.coverMs / 1000).toFixed(2)} s).`
            : `Saved frame: ${(post.coverMs / 1000).toFixed(2)} s — used by Instagram and TikTok.`}
          {!rendition && ' Conversion still running: preview shows the original video.'}
        </small>
        <div className="actions">
          <label className={`button-like small ${disabled ? 'disabled' : ''}`}>
            Upload image…
            <input
              type="file"
              accept="image/*"
              hidden
              disabled={disabled}
              onChange={(e) => {
                const f = e.target.files?.[0]
                e.target.value = ''
                if (f) onUpload(f)
              }}
            />
          </label>
          {post.coverCustom && (
            <button type="button" className="secondary outline small" disabled={disabled} onClick={onClearImage}>
              Remove image
            </button>
          )}
        </div>
      </div>
    </div>
  )
}
