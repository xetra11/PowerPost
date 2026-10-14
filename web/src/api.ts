// Types mirror the Go JSON (internal/app, internal/store, internal/platform).

export type Severity = 'error' | 'warning' | 'info'

export interface Issue {
  platform: string
  severity: Severity
  scope: 'content' | 'account'
  code: string
  message: string
  autoFix?: string
}

export interface MediaInfo {
  container: string
  width: number
  height: number
  fps: number
  duration: number
  videoCodec: string
  pixFmt: string
  bitrate: number
  hasVideo: boolean
  hasAudio: boolean
  audioCodec?: string
  audioSampleRate?: number
  audioChannels?: number
  size: number
  image?: boolean // a still image, not a video
}

export interface Rendition {
  id: number
  assetId: number
  platform: string
  status: 'pending' | 'processing' | 'ready' | 'failed'
  progress: number
  info: MediaInfo | null
  plan: { fixes: string[]; format?: string }
  error: string
  url: string
}

export type TargetStatus = 'idle' | 'queued' | 'publishing' | 'published' | 'failed' | 'removed'

export interface Target {
  id: number
  platform: string
  displayName: string
  enabled: boolean
  captionOverride: string | null
  hashtagsOverride: string[] | null
  options: Record<string, unknown>
  status: TargetStatus
  stage: string
  remoteId: string
  permalink: string
  note: string
  error: string
  publishedAt: string | null
  finalCaption: string
  captionLength: number
  captionMax: number
  rendition: Rendition | null // the video, or the first image
  renditions: Rendition[] // one per post media, in order
  issues: Issue[]
  notices: string[]
  canPublish: boolean
  embedUrl: string
}

export interface Stats {
  likes?: number
  comments?: number
  views?: number
  shares?: number
  note?: string
  at: string
}

export type PostStatus = 'draft' | 'approved' | 'publishing' | 'published' | 'partial' | 'failed'
export type AspectStrategy = 'blur' | 'crop' | 'pad'

export type PostKind = 'video' | 'image'

export interface Asset {
  id: number
  originalName: string
  info: MediaInfo
  position: number
  url: string
  thumbUrl: string
}

export interface ProjectRef {
  id: number
  slug: string
  name: string
}

export interface Post {
  id: number
  projectId: number
  project: ProjectRef
  title: string
  kind: PostKind // follows the attached media: one video, or images (a carousel when several)
  caption: string
  hashtags: string[]
  aspectStrategy: AspectStrategy
  coverMs: number
  coverCustom: boolean
  coverUrl: string
  coverFrameUrl: string
  status: PostStatus
  createdBy: 'ui' | 'mcp'
  createdAt: string
  updatedAt: string
  asset: Asset | null // the video, or the first image
  assets: Asset[]
  targets: Target[]
  issues: Issue[]
  ready: boolean
  approvalValid: boolean
  canPublish: boolean
  canPublishAll: boolean
  editUrl: string
  reviewUrl: string
}

export interface SchemaProp {
  type: 'boolean' | 'string'
  title?: string
  description?: string
  enum?: string[]
  'x-enumLabels'?: Record<string, string>
  default?: unknown
}

export interface Platform {
  id: string
  displayName: string
  configured: boolean
  connected: boolean
  accountName: string
  expiresAt: string | null
  redirectUri: string
  missingSettings: string[]
  canImportToken: boolean
  tokenPrompt: { label: string; help: string; only: boolean } | null
  constraints: { caption: { maxChars: number; countUtf16: boolean } }
  optionsSchema: { properties: Record<string, SchemaProp> }
}

export interface ProjectSettings {
  brand: { gameName: string; link: string; defaultHashtags: string[]; toneNotes: string }
  defaultPlatforms: string[]
  defaultAspect: AspectStrategy
}

export interface Project {
  id: number
  slug: string
  name: string
  settings: ProjectSettings
  postCount: number
  createdAt: string
}

/** Settings shared by all projects. */
export interface Settings {
  mcpAllowPublish: boolean
}

export interface SystemStatus {
  ffmpeg: string
  encoder: string
  inboxHostPath: string
  mcpUrl: string
  publicBaseUrl: string
}

export interface Attempt {
  id: number
  targetId: number
  startedAt: string
  finishedAt: string | null
  status: string
  error: string
  log: string
}

async function request<T>(method: string, url: string, body?: unknown): Promise<T> {
  const res = await fetch(url, {
    method,
    headers: body !== undefined ? { 'Content-Type': 'application/json' } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  const text = await res.text()
  const data = text ? JSON.parse(text) : null
  if (!res.ok) throw new Error(data?.error ?? `${res.status} ${res.statusText}`)
  return data as T
}

export const api = {
  status: () => request<SystemStatus>('GET', '/api/status'),
  projects: () => request<Project[]>('GET', '/api/projects'),
  createProject: (name: string) => request<Project>('POST', '/api/projects', { name }),
  updateProject: (id: number, patch: { name?: string; slug?: string; settings?: ProjectSettings }) =>
    request<Project>('PUT', `/api/projects/${id}`, patch),
  deleteProject: (id: number) => request<unknown>('DELETE', `/api/projects/${id}`),
  posts: (projectId: number) => request<Post[]>('GET', `/api/posts?project=${projectId}`),
  post: (id: number) => request<Post>('GET', `/api/posts/${id}`),
  createPost: (projectId: number) => request<Post>('POST', '/api/posts', { projectId }),
  updatePost: (id: number, patch: Partial<Pick<Post, 'title' | 'caption' | 'hashtags' | 'aspectStrategy' | 'coverMs'>>) =>
    request<Post>('PATCH', `/api/posts/${id}`, patch),
  deletePost: (id: number) => request<unknown>('DELETE', `/api/posts/${id}`),
  updateTarget: (id: number, platform: string, patch: TargetPatch) =>
    request<Post>('PATCH', `/api/posts/${id}/targets/${platform}`, patch),
  approve: (id: number) => request<Post>('POST', `/api/posts/${id}/approve`, {}),
  unapprove: (id: number) => request<Post>('POST', `/api/posts/${id}/unapprove`, {}),
  publish: (id: number, platforms: string[] = []) => request<Post>('POST', `/api/posts/${id}/publish`, { platforms }),
  duplicatePost: (id: number) => request<Post>('POST', `/api/posts/${id}/duplicate`, {}),
  stats: (id: number) => request<Record<string, Stats>>('GET', `/api/posts/${id}/stats`),
  checkLive: (id: number) => request<Post>('POST', `/api/posts/${id}/check-live`, {}),
  clearCover: (id: number) => request<Post>('DELETE', `/api/posts/${id}/cover`),
  removeMedia: (id: number, assetId: number) => request<Post>('DELETE', `/api/posts/${id}/media/${assetId}`),
  reorderMedia: (id: number, ids: number[]) => request<Post>('PUT', `/api/posts/${id}/media/order`, { ids }),
  attempts: (id: number) => request<Attempt[]>('GET', `/api/posts/${id}/attempts`),
  platforms: (projectId: number) => request<Platform[]>('GET', `/api/projects/${projectId}/platforms`),
  accountInfo: (projectId: number, platform: string) =>
    request<{ info: Record<string, unknown> | null }>('GET', `/api/projects/${projectId}/platforms/${platform}/account-info`),
  startConnect: (projectId: number, platform: string) => request<{ url: string }>('POST', `/api/projects/${projectId}/connections/${platform}/start`, {}),
  disconnect: (projectId: number, platform: string) => request<unknown>('DELETE', `/api/projects/${projectId}/connections/${platform}`),
  importToken: (projectId: number, platform: string, accessToken: string) =>
    request<unknown>('POST', `/api/projects/${projectId}/connections/${platform}/token`, { accessToken }),
  settings: () => request<Settings>('GET', '/api/settings'),
  saveSettings: (s: Settings) => request<Settings>('PUT', '/api/settings', s),
}

export interface TargetPatch {
  enabled?: boolean
  captionOverride?: string
  useBaseCaption?: boolean
  hashtagsOverride?: string[]
  useBaseHashtags?: boolean
  options?: Record<string, unknown>
}

/** Uploads a custom cover image. */
export async function uploadCover(id: number, file: File): Promise<Post> {
  const form = new FormData()
  form.append('file', file)
  const res = await fetch(`/api/posts/${id}/cover`, { method: 'POST', body: form })
  const data = await res.json().catch(() => null)
  if (!res.ok) throw new Error(data?.error ?? `Upload failed (${res.status})`)
  return data as Post
}

/**
 * Uploads a video or an image with progress (fetch can't report upload progress).
 * A video replaces the post's media; images are added unless replace is set.
 */
export function uploadMedia(id: number, file: File, onProgress: (fraction: number) => void, replace = false): Promise<Post> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    xhr.open('POST', `/api/posts/${id}/media${replace ? '?replace=1' : ''}`)
    xhr.upload.onprogress = (e) => e.lengthComputable && onProgress(e.loaded / e.total)
    xhr.onload = () => {
      let data: { error?: string } | null = null
      try {
        data = JSON.parse(xhr.responseText)
      } catch {
        /* not JSON */
      }
      if (xhr.status >= 200 && xhr.status < 300) resolve(data as unknown as Post)
      else reject(new Error(data?.error ?? `Upload failed (${xhr.status})`))
    }
    xhr.onerror = () => reject(new Error('Upload failed (network error)'))
    const form = new FormData()
    form.append('file', file)
    xhr.send(form)
  })
}

// ---- formatting helpers ----

export function formatDuration(sec: number): string {
  const s = Math.round(sec)
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`
}

export function formatBytes(n: number): string {
  if (n >= 1 << 30) return `${(n / (1 << 30)).toFixed(1)} GB`
  return `${(n / (1 << 20)).toFixed(1)} MB`
}

/** File format of a still image: JPEG, PNG, WEBP… */
export function imageFormat(i: MediaInfo): string {
  return (i.videoCodec === 'mjpeg' ? 'jpeg' : i.videoCodec).toUpperCase()
}

export function describeMedia(i: MediaInfo): string {
  if (i.image) return [`${i.width}×${i.height}`, imageFormat(i), formatBytes(i.size)].join(' · ')
  const parts = [`${i.width}×${i.height}`, `${Math.round(i.fps * 100) / 100} fps`, formatDuration(i.duration), formatBytes(i.size)]
  parts.push(i.hasAudio ? `${i.videoCodec}/${i.audioCodec}` : `${i.videoCodec}, no audio`)
  return parts.join(' · ')
}

/** Summary of several converted images, e.g. "3 images · 1080×1350 · JPEG · 2.1 MB". */
export function describeImages(infos: MediaInfo[]): string {
  if (infos.length === 1) return describeMedia(infos[0])
  const sizes = [...new Set(infos.map((i) => `${i.width}×${i.height}`))]
  const formats = [...new Set(infos.map(imageFormat))]
  const total = infos.reduce((n, i) => n + i.size, 0)
  return [`${infos.length} images`, sizes.length === 1 ? sizes[0] : 'mixed sizes', formats.join('/'), formatBytes(total)].join(' · ')
}

/** True while any media of an enabled platform is still being converted. */
export function isConverting(p: Post | null): boolean {
  return (
    !!p &&
    p.assets.length > 0 &&
    p.targets.some(
      (t) => t.enabled && (t.renditions.length < p.assets.length || t.renditions.some((r) => r.status === 'pending' || r.status === 'processing')),
    )
  )
}

export function parseHashtags(text: string): string[] {
  return text
    .split(/[\s,]+/)
    .map((t) => t.replace(/^#+/, '').trim())
    .filter(Boolean)
}

/** Mirrors platform.ComposeCaption on the server. */
export function composeCaption(text: string, tags: string[]): string {
  const body = text.trim()
  const lower = body.toLowerCase()
  const seen = new Set<string>()
  const add: string[] = []
  for (const t of tags) {
    const k = t.toLowerCase()
    if (seen.has(k)) continue
    seen.add(k)
    if (!lower.includes('#' + k)) add.push('#' + t)
  }
  if (add.length === 0) return body
  return body ? `${body}\n\n${add.join(' ')}` : add.join(' ')
}

export function captionLength(s: string, utf16: boolean): number {
  return utf16 ? s.length : [...s].length
}
