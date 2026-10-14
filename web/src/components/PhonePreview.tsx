import { useRef, useState, type ReactNode } from 'react'
import { isScreen, safeZones, surfaceOf, type Zone } from '../safeZones'

export type PreviewMode = 'live' | 'simulation' | 'safezones' | 'plain'

export interface PreviewImage {
  url: string
  width: number
  height: number
}

export interface PreviewProps {
  platform: string // instagram | tiktok | youtube | other
  videoUrl: string | null // null while converting
  images?: PreviewImage[] | null // image posts: the converted images, null while converting
  placeholder: string
  caption: string
  title?: string // YouTube Shorts show the video title, not the caption
  username: string // without @
  label?: string // e.g. "Promotional content"
  mode: PreviewMode
}

/**
 * A phone-sized preview of the converted video (or images, as a swipeable carousel). In "simulation" mode it draws an
 * approximation of the app around it: the Reels / TikTok / Shorts feed UI on top of
 * videos and TikTok photos, the Instagram feed around image posts. Not
 * pixel-exact: the apps change their layout regularly.
 */
export function PhonePreview(p: PreviewProps) {
  const videoRef = useRef<HTMLVideoElement>(null)
  const [paused, setPaused] = useState(true)
  const [muted, setMuted] = useState(true)
  const simulate = p.mode === 'simulation' && (p.platform === 'instagram' || p.platform === 'tiktok' || p.platform === 'youtube')

  if (p.images) {
    const surface = surfaceOf(p.platform, 'image')
    if (simulate && surface === 'feed') return <InstagramFeed {...p} images={p.images} />
    const carousel = p.images.length > 1
    return (
      <div className={simulate ? `phone sim sim-${p.platform}` : 'phone'}>
        <ImageCarousel
          images={p.images}
          zones={p.mode === 'safezones' && surface === 'feed' ? (aspect) => safeZones('feed', { aspect, carousel }) : undefined}
        />
        {p.mode === 'safezones' && isScreen(surface) && <SafeZoneOverlay zones={safeZones(surface)} />}
        {simulate && <TikTokOverlay {...p} />}
      </div>
    )
  }

  if (!p.videoUrl) {
    return (
      <div className="phone">
        <div className="phone-placeholder">{p.placeholder}</div>
      </div>
    )
  }

  if (!simulate) {
    return (
      <div className="phone">
        <video src={p.videoUrl} controls playsInline preload="metadata" />
        {p.mode === 'safezones' && <SafeZoneOverlay zones={safeZones(surfaceOf(p.platform, 'video'))} />}
      </div>
    )
  }

  const toggle = () => {
    const v = videoRef.current
    if (!v) return
    if (v.paused) v.play().catch(() => {})
    else v.pause()
  }

  return (
    <div className={`phone sim sim-${p.platform}`}>
      <video
        ref={videoRef}
        src={p.videoUrl + '#t=0.1'} // makes browsers paint the first frame before playback
        playsInline
        loop
        muted={muted}
        preload="auto"
        onPlay={() => setPaused(false)}
        onPause={() => setPaused(true)}
        onClick={toggle}
      />
      {paused && (
        <button type="button" className="sim-play" onClick={toggle} aria-label="Play">
          <Icon name="play" />
        </button>
      )}
      <button type="button" className="sim-mute" onClick={() => setMuted(!muted)} aria-label={muted ? 'Unmute' : 'Mute'}>
        <Icon name={muted ? 'muted' : 'sound'} />
      </button>
      {p.platform === 'instagram' ? <InstagramOverlay {...p} /> : p.platform === 'youtube' ? <ShortsOverlay {...p} /> : <TikTokOverlay {...p} />}
    </div>
  )
}

function InstagramOverlay({ caption, username, label }: PreviewProps) {
  return (
    <div className="sim-overlay" aria-hidden>
      <div className="sim-top ig-top">
        <strong>Reels</strong>
        <Icon name="camera" />
      </div>
      <div className="sim-rail">
        <RailItem icon="heart" text="1.204" />
        <RailItem icon="comment" text="48" />
        <RailItem icon="send" text="" />
        <RailItem icon="dots" text="" />
        <div className="ig-audio-thumb" />
      </div>
      <div className="sim-bottom">
        <div className="sim-user">
          <span className="avatar" />
          <strong>{username || 'your_account'}</strong>
          <span className="ig-follow">Follow</span>
        </div>
        {label && <div className="sim-label">{label}</div>}
        <Caption text={caption} />
        <div className="sim-music">
          <Icon name="music" /> {username || 'your_account'} · Original audio
        </div>
      </div>
    </div>
  )
}

function TikTokOverlay({ caption, username, label }: PreviewProps) {
  return (
    <div className="sim-overlay" aria-hidden>
      <div className="sim-top tt-top">
        <span className="tt-tab">Following</span>
        <span className="tt-tab active">For You</span>
        <Icon name="search" />
      </div>
      <div className="sim-rail tt-rail">
        <div className="tt-avatar">
          <span className="avatar" />
          <span className="tt-plus">+</span>
        </div>
        <RailItem icon="heart" text="12.4K" />
        <RailItem icon="comment" text="318" />
        <RailItem icon="bookmark" text="902" />
        <RailItem icon="share" text="211" />
        <div className="tt-disc" />
      </div>
      <div className="sim-bottom tt-bottom">
        <strong className="tt-user">{username || 'your_account'}</strong>
        <Caption text={caption} />
        {label && <div className="sim-label tt-label">{label}</div>}
        <div className="sim-music">
          <Icon name="music" /> <span className="marquee">original sound - {username || 'your_account'}</span>
        </div>
      </div>
      <div className="tt-nav">
        <span>Home</span>
        <span>Friends</span>
        <span className="tt-create">+</span>
        <span>Inbox</span>
        <span>Profile</span>
      </div>
    </div>
  )
}

/** YouTube Shorts: channel, title and sound at the bottom, buttons on the right. The description isn't shown in the feed. */
function ShortsOverlay({ title, username }: PreviewProps) {
  const handle = (username || 'your_channel').replace(/\s+/g, '')
  return (
    <div className="sim-overlay" aria-hidden>
      <div className="sim-top yt-top">
        <Icon name="search" />
        <Icon name="dots" />
      </div>
      <div className="sim-rail yt-rail">
        <RailItem icon="like" text="1.2K" />
        <RailItem icon="dislike" text="Dislike" />
        <RailItem icon="comment" text="48" />
        <RailItem icon="share" text="Share" />
        <RailItem icon="remix" text="Remix" />
        <span className="yt-sound-thumb" />
      </div>
      <div className="sim-bottom yt-bottom">
        <div className="sim-user">
          <span className="avatar" />
          <strong>@{handle}</strong>
          <span className="yt-subscribe">Subscribe</span>
        </div>
        {title && <Caption text={title} />}
        <div className="sim-music">
          <Icon name="music" /> <span className="marquee">Original sound · {username || 'your_channel'}</span>
        </div>
      </div>
      <div className="tt-nav yt-nav">
        <span>Home</span>
        <span>Shorts</span>
        <span className="yt-create">+</span>
        <span>Subscriptions</span>
        <span>You</span>
      </div>
    </div>
  )
}

/**
 * An Instagram feed post: header above the image, buttons and caption below.
 * Nothing covers the image itself; carousels show dots under it.
 */
function InstagramFeed(p: PreviewProps & { images: PreviewImage[] }) {
  const [index, setIndex] = useState(0)
  const name = p.username || 'your_account'
  const first = p.images[0]
  return (
    <div className="phone sim sim-feed">
      <div className="feed">
        <div className="feed-top" aria-hidden>
          <strong className="feed-logo">Instagram</strong>
          <span>
            <Icon name="heart" />
            <Icon name="send" />
          </span>
        </div>
        <div className="feed-head" aria-hidden>
          <span className="avatar" />
          <strong>{name}</strong>
          <Icon name="dots" />
        </div>
        <div className="feed-media" style={{ aspectRatio: `${first.width} / ${first.height}` }}>
          <ImageCarousel images={p.images} index={index} onIndex={setIndex} dots={false} />
        </div>
        <div className="feed-actions" aria-hidden>
          <Icon name="heart" />
          <Icon name="comment" />
          <Icon name="send" />
          {p.images.length > 1 && (
            <span className="feed-dots">
              {p.images.map((img, n) => (
                <span key={img.url} className={n === index ? 'active' : ''} />
              ))}
            </span>
          )}
          <span className="feed-save">
            <Icon name="bookmark" />
          </span>
        </div>
        <div className="feed-likes">1.204 likes</div>
        <Caption text={p.caption} prefix={name} />
        <div className="feed-comments">View all 48 comments</div>
      </div>
      <div className="feed-nav" aria-hidden>
        <Icon name="home" />
        <Icon name="search" />
        <Icon name="plus" />
        <Icon name="reels" />
        <span className="avatar" />
      </div>
    </div>
  )
}

/** Covered areas of the screen (or image), labelled. */
export function SafeZoneOverlay({ zones }: { zones: Zone[] }) {
  return (
    <div className="safe-zones" aria-hidden>
      {zones.map((z, i) => (
        <div
          key={i}
          className={z.width < 20 && z.height > 2 * z.width ? 'vertical' : undefined}
          style={{ top: `${z.top}%`, left: `${z.left}%`, width: `${z.width}%`, height: `${z.height}%` }}
        >
          {z.label && <span>{z.label}</span>}
        </div>
      ))}
    </div>
  )
}

/** Caption clamped to two lines with "… more", like both apps; hashtags in bold. */
function Caption({ text, prefix }: { text: string; prefix?: string }) {
  const [open, setOpen] = useState(false)
  if (!text.trim()) return null
  const parts: ReactNode[] = text.split(/(#[\p{L}\p{N}_]+|@[\p{L}\p{N}_.]+)/u).map((s, i) =>
    s.startsWith('#') || s.startsWith('@') ? <b key={i}>{s}</b> : s,
  )
  return (
    <div className={`sim-caption ${open ? 'open' : ''}`} onClick={() => setOpen(!open)} title={open ? 'Collapse' : 'Show full caption'}>
      <span className="text">
        {prefix && <strong>{prefix} </strong>}
        {parts}
      </span>
      {!open && <span className="more">… more</span>}
    </div>
  )
}

function RailItem({ icon, text }: { icon: IconName; text: string }) {
  return (
    <div className="rail-item">
      <Icon name={icon} />
      {text && <small>{text}</small>}
    </div>
  )
}

type IconName =
  | 'heart'
  | 'comment'
  | 'send'
  | 'share'
  | 'bookmark'
  | 'dots'
  | 'music'
  | 'camera'
  | 'search'
  | 'play'
  | 'muted'
  | 'sound'
  | 'home'
  | 'plus'
  | 'reels'
  | 'like'
  | 'dislike'
  | 'remix'

/** Generic line icons (not the platforms' own artwork). */
function Icon({ name }: { name: IconName }) {
  const paths: Record<IconName, ReactNode> = {
    heart: <path d="M12 20s-7-4.4-9.3-8.6C1 8.2 2.8 4.5 6.4 4.5c2.2 0 3.5 1.3 4.1 2.4h3c.6-1.1 1.9-2.4 4.1-2.4 3.6 0 5.4 3.7 3.7 6.9C19 15.6 12 20 12 20z" />,
    comment: <path d="M4 5h16v11H9l-5 4V5z" />,
    send: <path d="M3 11l18-8-7 18-3-7-8-3z" />,
    share: <path d="M14 4l7 7-7 7v-4c-5 0-8 1.5-11 5 1-6 4-10 11-10V4z" />,
    bookmark: <path d="M6 3h12v18l-6-4-6 4V3z" />,
    dots: (
      <>
        <circle cx="5" cy="12" r="1.6" />
        <circle cx="12" cy="12" r="1.6" />
        <circle cx="19" cy="12" r="1.6" />
      </>
    ),
    music: <path d="M9 18V6l10-2v12M9 18a2.5 2.5 0 1 1-5 0 2.5 2.5 0 0 1 5 0zm10-2a2.5 2.5 0 1 1-5 0 2.5 2.5 0 0 1 5 0z" />,
    camera: <path d="M4 7h4l2-2h4l2 2h4v12H4V7zm8 3a4 4 0 1 0 0 8 4 4 0 0 0 0-8z" />,
    search: <path d="M10.5 4a6.5 6.5 0 1 1 0 13 6.5 6.5 0 0 1 0-13zM15.5 15.5L20 20" />,
    play: <path d="M8 5l12 7-12 7V5z" />,
    muted: <path d="M4 9h4l5-4v14l-5-4H4V9zm12 0l5 6m0-6l-5 6" />,
    sound: <path d="M4 9h4l5-4v14l-5-4H4V9zm12-1a5 5 0 0 1 0 8m2-11a9 9 0 0 1 0 14" />,
    home: <path d="M4 11l8-7 8 7v9h-5v-6H9v6H4v-9z" />,
    plus: <path d="M5 4h14a1 1 0 0 1 1 1v14a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1zm7 4v8m-4-4h8" />,
    reels: <path d="M4 4h16v16H4V4zm0 5h16M9 4l2 5m3-5l2 5m-6 3v5l4-2.5-4-2.5z" />,
    like: <path d="M7 10v10H4V10h3zm0 0l4-7c1.5 0 2.5 1 2 3l-1 4h6a2 2 0 0 1 2 2.3l-1.2 6A2 2 0 0 1 16.8 20H7" />,
    dislike: <path d="M7 14V4H4v10h3zm0 0l4 7c1.5 0 2.5-1 2-3l-1-4h6a2 2 0 0 0 2-2.3l-1.2-6A2 2 0 0 0 16.8 4H7" />,
    remix: <path d="M4 7h11l-3-3m3 3l-3 3M20 17H9l3 3m-3-3l3-3" />,
  }
  const filled = name === 'play' || name === 'dots'
  return (
    <svg viewBox="0 0 24 24" className="sim-icon" fill={filled ? 'currentColor' : 'none'} stroke="currentColor" strokeWidth="1.8" strokeLinejoin="round" strokeLinecap="round">
      {paths[name]}
    </svg>
  )
}

/**
 * Shows one image at a time with arrows and dots, like a carousel post. The
 * image is fitted into the available space; zones (in percent of the image)
 * are drawn on top of it.
 */
function ImageCarousel({
  images,
  index: controlled,
  onIndex,
  dots = true,
  zones,
}: {
  images: PreviewImage[]
  index?: number
  onIndex?: (i: number) => void
  dots?: boolean
  zones?: (aspect: number) => Zone[]
}) {
  const [own, setOwn] = useState(0)
  const index = controlled ?? own
  const setIndex = onIndex ?? setOwn
  const urls = images.map((x) => x.url)
  const i = Math.min(index, urls.length - 1)
  const aspect = images[i].width / images[i].height || 1
  return (
    <div className="carousel">
      <div className="media-fit">
        <div className="media-box" style={{ aspectRatio: `${aspect}`, ...(aspect >= 9 / 16 ? { width: '100%' } : { height: '100%' }) }}>
          <img src={urls[i]} alt={`Image ${i + 1} of ${urls.length}`} />
          {zones && <SafeZoneOverlay zones={zones(aspect)} />}
        </div>
      </div>
      {urls.length > 1 && (
        <>
          <button type="button" className="carousel-nav prev" disabled={i === 0} onClick={() => setIndex(i - 1)} aria-label="Previous image">
            ‹
          </button>
          <button type="button" className="carousel-nav next" disabled={i === urls.length - 1} onClick={() => setIndex(i + 1)} aria-label="Next image">
            ›
          </button>
          <span className="carousel-count">
            {i + 1}/{urls.length}
          </span>
          {dots && (
            <span className="carousel-dots" aria-hidden>
              {urls.map((u, n) => (
                <span key={u} className={n === i ? 'active' : ''} />
              ))}
            </span>
          )}
        </>
      )}
    </div>
  )
}
