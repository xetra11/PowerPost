import { useEffect, useRef, useState } from 'react'
import type { Asset, Frame, Platform, Post } from '../api'
import { aspectChoices, canvasAspect, isScreen, safeZones, surfaceNote, surfaceOf } from '../safeZones'
import { SafeZoneOverlay } from './PhonePreview'

const MIN_SCALE = 0.25
const MAX_SCALE = 4

/**
 * Lets you place the video (or each image) by hand on a platform's canvas:
 * drag to move, wheel or slider to zoom, with that platform's safe zones on
 * top. Saving re-converts the media for the chosen platforms.
 */
export function FramingEditor({
  post,
  platforms,
  disabled,
  onSave,
}: {
  post: Post
  platforms: Platform[]
  disabled: boolean
  onSave: (assetId: number, platforms: string[], frame: Frame | null) => Promise<void>
}) {
  const kind = post.kind
  const targets = post.targets.filter((t) => t.enabled && platforms.find((p) => p.id === t.platform)?.supportedKinds.includes(kind))
  const [platformId, setPlatformId] = useState(targets[0]?.platform ?? '')
  const [assetIndex, setAssetIndex] = useState(0)
  const [zones, setZones] = useState(true)
  const [applyAll, setApplyAll] = useState(false)
  const [saving, setSaving] = useState(false)

  const target = targets.find((t) => t.platform === platformId) ?? targets[0]
  const platform = platforms.find((p) => p.id === target?.platform)
  const asset: Asset | undefined = post.assets[Math.min(assetIndex, post.assets.length - 1)]
  const stored = (target && asset?.framing?.[target.platform]) || null

  const surface = surfaceOf(target?.platform ?? '', kind)
  const first = post.assets[0]
  const sameAspect = kind === 'image' && !!platform?.imageConstraints?.sameAspect
  const followsFirst = sameAspect && asset !== first

  const ma = asset ? asset.info.width / asset.info.height : 1
  const automatic = (aspect: number): Frame => ({ scale: post.aspectStrategy === 'crop' ? Math.max(ma / aspect, aspect / ma) : 1, x: 0.5, y: 0.5 })

  const [draft, setDraft] = useState<Frame | null>(null)
  const key = `${target?.platform}:${asset?.id}:${JSON.stringify(stored)}:${post.aspectStrategy}`
  useEffect(() => setDraft(null), [key]) // a saved or switched frame replaces the draft

  if (!target || !platform || !asset) return null

  // Carousel images on SameAspect platforms take the first image's canvas.
  const aspectFor = (f: Frame | null) =>
    followsFirst
      ? canvasAspect(platform, kind, { width: first.info.width, height: first.info.height, frame: first.framing?.[platform.id] })
      : canvasAspect(platform, kind, { width: asset.info.width, height: asset.info.height, frame: f ?? undefined })
  const frame: Frame = draft ?? stored ?? automatic(aspectFor(null))
  const ca = aspectFor(draft ?? stored)
  const dirty = draft !== null
  const rendition = target.renditions.find((r) => r.assetId === asset.id)

  const set = (patch: Partial<Frame>) => setDraft({ ...frame, ...patch })
  const save = async (f: Frame | null) => {
    setSaving(true)
    try {
      const ids = applyAll ? targets.map((t) => t.platform) : [target.platform]
      await onSave(asset.id, ids, f)
      setDraft(null)
    } finally {
      setSaving(false)
    }
  }

  return (
    <article className="framing">
      <header className="platform-head">
        <strong>Framing &amp; safe zones</strong>
        <div role="group" className="mode-switch" aria-label="Platform">
          {targets.map((t) => (
            <button key={t.platform} type="button" className={t.platform === target.platform ? 'small' : 'small secondary outline'} onClick={() => setPlatformId(t.platform)}>
              {t.displayName}
              {asset.framing?.[t.platform] ? ' •' : ''}
            </button>
          ))}
        </div>
      </header>
      {post.assets.length > 1 && (
        <div className="framing-images" aria-label="Image">
          {post.assets.map((a, i) => (
            <button key={a.id} type="button" className={a === asset ? 'active' : ''} onClick={() => setAssetIndex(i)} aria-label={`Image ${i + 1}`}>
              <img src={a.thumbUrl || a.url} alt="" />
              <span>{i + 1}</span>
            </button>
          ))}
        </div>
      )}
      <div className="framing-body">
        <Stage
          asset={asset}
          kind={kind}
          frame={frame}
          canvasAspect={ca}
          screen={isScreen(surface)}
          blur={post.aspectStrategy === 'blur'}
          zones={zones ? safeZones(surface, { aspect: ca, carousel: post.assets.length > 1 }) : []}
          disabled={disabled}
          onChange={set}
        />
        <div className="framing-controls">
          <small className="muted">{surfaceNote[surface]}</small>
          <label>
            Zoom <span className="muted">{Math.round(frame.scale * 100)} %</span>
            <input
              type="range"
              min={Math.log(MIN_SCALE)}
              max={Math.log(MAX_SCALE)}
              step={0.01}
              value={Math.log(frame.scale)}
              disabled={disabled}
              onChange={(e) => set({ scale: Math.exp(Number(e.target.value)) })}
            />
          </label>
          <div className="actions">
            <button type="button" className="small secondary outline" disabled={disabled} onClick={() => set({ scale: 1, x: 0.5, y: 0.5 })} title="Show all of it">
              Fit
            </button>
            <button type="button" className="small secondary outline" disabled={disabled} onClick={() => set({ scale: Math.max(ma / ca, ca / ma), x: 0.5, y: 0.5 })} title="Fill the canvas, crop the rest">
              Fill
            </button>
            <button type="button" className="small secondary outline" disabled={disabled} onClick={() => set({ x: 0.5, y: 0.5 })}>
              Center
            </button>
          </div>
          {kind === 'image' && (
            <label>
              Format
              <select
                value={followsFirst ? '' : String(frame.aspect ?? 0)}
                disabled={disabled || followsFirst}
                onChange={(e) => set({ aspect: Number(e.target.value) || undefined })}
              >
                {followsFirst && <option value="">Same as image 1</option>}
                {aspectChoices(platform).map((o) => (
                  <option key={o.label} value={String(o.value)}>
                    {o.label}
                  </option>
                ))}
              </select>
              {followsFirst && <small className="hint">{platform.displayName} crops every carousel image to the first one's format.</small>}
            </label>
          )}
          <label>
            <input type="checkbox" checked={zones} onChange={(e) => setZones(e.target.checked)} />
            Show safe zones
          </label>
          {targets.length > 1 && (
            <label>
              <input type="checkbox" checked={applyAll} disabled={disabled} onChange={(e) => setApplyAll(e.target.checked)} />
              Apply to all platforms
            </label>
          )}
          <div className="actions">
            <button type="button" className="small" disabled={disabled || !dirty || saving} aria-busy={saving} onClick={() => save(frame)}>
              Apply framing
            </button>
            {dirty && (
              <button type="button" className="small secondary outline" disabled={saving} onClick={() => setDraft(null)}>
                Discard
              </button>
            )}
            {stored && !dirty && (
              <button type="button" className="small secondary outline" disabled={disabled || saving} onClick={() => save(null)}>
                Back to automatic
              </button>
            )}
          </div>
          <small className="muted">
            {dirty
              ? 'Not applied yet.'
              : stored
                ? 'Custom framing' + (rendition && rendition.status !== 'ready' ? ` — converting… ${Math.round(rendition.progress * 100)} %` : ' applied.')
                : `Automatic (${post.aspectStrategy === 'crop' ? 'crop to fill' : post.aspectStrategy === 'pad' ? 'black bars' : 'blurred background'}). Drag the ${kind} to move it.`}
          </small>
        </div>
      </div>
    </article>
  )
}

/** The canvas (inside a 9:16 phone screen where the app shows it full screen) with the draggable media. */
function Stage({
  asset,
  kind,
  frame,
  canvasAspect: ca,
  screen,
  blur,
  zones,
  disabled,
  onChange,
}: {
  asset: Asset
  kind: Post['kind']
  frame: Frame
  canvasAspect: number
  screen: boolean
  blur: boolean
  zones: ReturnType<typeof safeZones>
  disabled: boolean
  onChange: (patch: Partial<Frame>) => void
}) {
  const canvasRef = useRef<HTMLDivElement>(null)
  const drag = useRef<{ x: number; y: number; fx: number; fy: number } | null>(null)
  const latest = useRef({ frame, onChange, disabled })
  latest.current = { frame, onChange, disabled }

  const ma = asset.info.width / asset.info.height
  // Media size in canvas widths / heights (same math as internal/media/frame.go).
  const w = (ma > ca ? 1 : ma / ca) * frame.scale
  const h = (ma > ca ? ca / ma : 1) * frame.scale
  const clampX = (x: number) => Math.min(Math.max(x, Math.min(w, 1) * 0.1 - w / 2), 1 - Math.min(w, 1) * 0.1 + w / 2)
  const clampY = (y: number) => Math.min(Math.max(y, Math.min(h, 1) * 0.1 - h / 2), 1 - Math.min(h, 1) * 0.1 + h / 2)
  const x = clampX(frame.x)
  const y = clampY(frame.y)

  // Wheel zoom needs a non-passive listener to keep the page from scrolling.
  useEffect(() => {
    const el = canvasRef.current
    if (!el) return
    const onWheel = (e: WheelEvent) => {
      const { frame, onChange, disabled } = latest.current
      if (disabled) return
      e.preventDefault()
      onChange({ scale: Math.min(Math.max(frame.scale * Math.exp(-e.deltaY * 0.0015), MIN_SCALE), MAX_SCALE) })
    }
    el.addEventListener('wheel', onWheel, { passive: false })
    return () => el.removeEventListener('wheel', onWheel)
  }, [])

  const style = { left: `${(x - w / 2) * 100}%`, top: `${(y - h / 2) * 100}%`, width: `${w * 100}%`, height: `${h * 100}%` }
  const canvas = (
    <div
      ref={canvasRef}
      className={`framing-canvas ${disabled ? '' : 'draggable'}`}
      style={{ aspectRatio: `${ca}`, ...(ca >= (screen ? 9 / 16 : 0.55) ? { width: '100%' } : { height: screen ? '100%' : '540px' }) }}
      onPointerDown={(e) => {
        if (disabled) return
        e.currentTarget.setPointerCapture(e.pointerId)
        drag.current = { x: e.clientX, y: e.clientY, fx: x, fy: y }
      }}
      onPointerMove={(e) => {
        const d = drag.current
        const rect = canvasRef.current?.getBoundingClientRect()
        if (!d || !rect) return
        onChange({ x: clampX(d.fx + (e.clientX - d.x) / rect.width), y: clampY(d.fy + (e.clientY - d.y) / rect.height) })
      }}
      onPointerUp={() => (drag.current = null)}
      onPointerCancel={() => (drag.current = null)}
    >
      {blur && <img className="framing-bg" src={kind === 'video' ? asset.thumbUrl : asset.url} alt="" draggable={false} />}
      {kind === 'video' ? (
        <video className="framing-media" style={style} src={asset.url} poster={asset.thumbUrl || undefined} muted loop autoPlay playsInline />
      ) : (
        <img className="framing-media" style={style} src={asset.url} alt="" draggable={false} />
      )}
      {!screen && zones.length > 0 && <SafeZoneOverlay zones={zones} />}
    </div>
  )
  if (!screen) return <div className="framing-stage plain">{canvas}</div>
  return (
    <div className="framing-stage">
      <div className="phone framing-phone">
        <div className="media-fit">{canvas}</div>
        {zones.length > 0 && <SafeZoneOverlay zones={zones} />}
      </div>
    </div>
  )
}
