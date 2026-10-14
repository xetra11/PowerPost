import type { Frame, Platform, PostKind } from './api'

/**
 * Where a post is shown:
 * - "reel": Instagram Reel, full screen 9:16 with UI on top
 * - "tiktok-video" / "tiktok-photo": TikTok feed, full screen; photos are letterboxed on black
 * - "shorts": YouTube Short, full screen 9:16 with UI on top
 * - "feed": Instagram feed post, UI above and below the image, nothing on top of it
 * - "plain": no app UI on top (Discord)
 */
export type Surface = 'reel' | 'tiktok-video' | 'tiktok-photo' | 'shorts' | 'feed' | 'plain'

export function surfaceOf(platform: string, kind: PostKind): Surface {
  if (platform === 'instagram') return kind === 'image' ? 'feed' : 'reel'
  if (platform === 'tiktok') return kind === 'image' ? 'tiktok-photo' : 'tiktok-video'
  if (platform === 'youtube') return 'shorts'
  return 'plain'
}

/** Surfaces drawn as a full 9:16 phone screen; the media is fitted into it. */
export const isScreen = (s: Surface) => s === 'reel' || s === 'tiktok-video' || s === 'tiktok-photo' || s === 'shorts'

/** A covered area in percent of the screen (or, for "feed", of the image). */
export interface Zone {
  top: number
  left: number
  width: number
  height: number
  label: string
}

/**
 * Areas covered by the app's UI. Approximations of the platforms' published
 * guides (Meta: keep the top 14 % and bottom 35 % of a Reel free; TikTok: about
 * 130 px top, 480 px bottom and 140 px right of 1080x1920); YouTube Shorts: about 8 %
 * top, 30 % bottom and 15 % right). Both apps change
 * their layout regularly, so leave some margin.
 *
 * For "feed" the zones are on the image: Instagram's profile grid shows a 3:4
 * crop of it, and carousels have a counter in the top right corner.
 */
export function safeZones(surface: Surface, opts: { aspect?: number; carousel?: boolean } = {}): Zone[] {
  switch (surface) {
    case 'reel':
      return [
        { top: 0, left: 0, width: 100, height: 14, label: 'Reels header' },
        { top: 65, left: 0, width: 100, height: 35, label: 'Account, caption & audio' },
        { top: 40, left: 84, width: 16, height: 25, label: 'Buttons' },
      ]
    case 'tiktok-video':
      return [
        { top: 0, left: 0, width: 100, height: 7, label: 'Tabs' },
        { top: 75, left: 0, width: 100, height: 25, label: 'Caption, account & sound' },
        { top: 33, left: 87, width: 13, height: 42, label: 'Buttons' },
      ]
    case 'tiktok-photo':
      // Photo mode adds the page dots and a title line above the caption.
      return [
        { top: 0, left: 0, width: 100, height: 7, label: 'Tabs' },
        { top: 70, left: 0, width: 100, height: 30, label: 'Dots, caption, account & sound' },
        { top: 30, left: 87, width: 13, height: 40, label: 'Buttons' },
      ]
    case 'shorts':
      return [
        { top: 0, left: 0, width: 100, height: 8, label: 'Search & menu' },
        { top: 70, left: 0, width: 100, height: 30, label: 'Title, channel & sound' },
        { top: 38, left: 85, width: 15, height: 32, label: 'Buttons' },
      ]
    case 'feed': {
      const zones: Zone[] = []
      const a = opts.aspect ?? 0.8
      if (a > 0.76) {
        const side = ((1 - 0.75 / a) / 2) * 100
        zones.push({ top: 0, left: 0, width: side, height: 100, label: 'Cut in profile grid' })
        zones.push({ top: 0, left: 100 - side, width: side, height: 100, label: '' })
      }
      if (opts.carousel) zones.push({ top: 2, left: 82, width: 16, height: 7, label: '1/N' })
      return zones
    }
    default:
      return []
  }
}

export const surfaceNote: Record<Surface, string> = {
  reel: 'Instagram Reel: the app UI covers the red areas.',
  'tiktok-video': 'TikTok video: the app UI covers the red areas.',
  'tiktok-photo': "TikTok photo: shown full screen, letterboxed on black if it isn't 9:16; the UI covers the red areas.",
  shorts: 'YouTube Short: the app UI covers the red areas.',
  feed: 'Instagram feed post: nothing covers the image, but the profile grid shows only a 3:4 crop of it.',
  plain: 'Shown without app UI on top.',
}

/** The canvas aspect ratio (width / height) of one media file on one platform, as the server computes it. */
export function canvasAspect(p: Platform, kind: PostKind, media: { width: number; height: number; frame?: Frame }): number {
  if (kind === 'video') {
    const v = p.constraints.video
    return v ? v.targetWidth / v.targetHeight : 9 / 16
  }
  const c = p.imageConstraints
  const a = media.frame?.aspect || media.width / media.height
  return Math.min(Math.max(a, c?.aspectMin || 0), c?.aspectMax || Infinity)
}

/** Canvas aspect ratios one can pick for an image on a platform (0 = automatic). */
export function aspectChoices(p: Platform): { value: number; label: string }[] {
  const c = p.imageConstraints
  const all = [
    { value: 9 / 16, label: '9:16 (full screen)' },
    { value: 3 / 4, label: '3:4' },
    { value: 4 / 5, label: '4:5 (portrait)' },
    { value: 1, label: '1:1 (square)' },
    { value: 1.91, label: '1.91:1 (landscape)' },
  ]
  return [
    { value: 0, label: 'Automatic' },
    ...all.filter((o) => o.value >= (c?.aspectMin || 0) - 0.001 && o.value <= (c?.aspectMax || Infinity) + 0.001),
  ]
}
