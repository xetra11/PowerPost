import { useRef, useState, type ReactNode } from 'react'

export type PreviewMode = 'simulation' | 'safezones' | 'plain'

export interface PreviewProps {
  platform: string // instagram | tiktok | other
  videoUrl: string | null // null while converting
  placeholder: string
  caption: string
  username: string // without @
  label?: string // e.g. "Promotional content"
  mode: PreviewMode
}

/**
 * A phone-sized preview of the converted video. In "simulation" mode it draws an
 * approximation of the Instagram Reels / TikTok feed UI on top, so you can see
 * what the caption, username and buttons cover. Not pixel-exact: both apps
 * change their layout regularly.
 */
export function PhonePreview(p: PreviewProps) {
  const videoRef = useRef<HTMLVideoElement>(null)
  const [paused, setPaused] = useState(true)
  const [muted, setMuted] = useState(true)
  const simulate = p.mode === 'simulation' && (p.platform === 'instagram' || p.platform === 'tiktok')

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
        {p.mode === 'safezones' && <SafeZones />}
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
      {p.platform === 'instagram' ? <InstagramOverlay {...p} /> : <TikTokOverlay {...p} />}
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

/** Caption clamped to two lines with "… more", like both apps; hashtags in bold. */
function Caption({ text }: { text: string }) {
  const [open, setOpen] = useState(false)
  if (!text.trim()) return null
  const parts: ReactNode[] = text.split(/(#[\p{L}\p{N}_]+|@[\p{L}\p{N}_.]+)/u).map((s, i) =>
    s.startsWith('#') || s.startsWith('@') ? <b key={i}>{s}</b> : s,
  )
  return (
    <div className={`sim-caption ${open ? 'open' : ''}`} onClick={() => setOpen(!open)} title={open ? 'Collapse' : 'Show full caption'}>
      <span className="text">{parts}</span>
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

function SafeZones() {
  return (
    <div className="safe-zones" aria-hidden>
      <div className="sz-top" />
      <div className="sz-bottom" />
      <div className="sz-right" />
    </div>
  )
}

type IconName = 'heart' | 'comment' | 'send' | 'share' | 'bookmark' | 'dots' | 'music' | 'camera' | 'search' | 'play' | 'muted' | 'sound'

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
  }
  const filled = name === 'play' || name === 'dots'
  return (
    <svg viewBox="0 0 24 24" className="sim-icon" fill={filled ? 'currentColor' : 'none'} stroke="currentColor" strokeWidth="1.8" strokeLinejoin="round" strokeLinecap="round">
      {paths[name]}
    </svg>
  )
}
