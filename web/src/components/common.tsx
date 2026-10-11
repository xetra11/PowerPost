import { useState } from 'react'
import type { Issue, PostStatus, SchemaProp, Target, TargetStatus } from '../api'

const statusLabels: Record<string, string> = {
  draft: 'Draft',
  approved: 'Approved',
  publishing: 'Publishing…',
  published: 'Published',
  partial: 'Partly published',
  failed: 'Failed',
  idle: 'Not published',
  queued: 'Queued',
}

export function StatusBadge({ status }: { status: PostStatus | TargetStatus }) {
  return <span className={`badge badge-${status}`}>{statusLabels[status] ?? status}</span>
}

/** One-line state of a platform target for lists and headers. */
export function targetSummary(t: Target): { icon: string; text: string; tone: 'ok' | 'warn' | 'error' | 'busy' } {
  if (t.status === 'published') return { icon: '✓', text: 'Published', tone: 'ok' }
  if (t.status === 'queued' || t.status === 'publishing') return { icon: '↻', text: t.stage || 'Publishing', tone: 'busy' }
  if (t.status === 'failed') return { icon: '✗', text: 'Publish failed', tone: 'error' }
  const r = t.rendition
  if (r && (r.status === 'pending' || r.status === 'processing')) return { icon: '↻', text: `Converting ${Math.round(r.progress * 100)}%`, tone: 'busy' }
  const errors = t.issues.filter((i) => i.severity === 'error')
  if (errors.some((i) => i.scope === 'content')) return { icon: '✗', text: `${errors.length} to fix`, tone: 'error' }
  if (errors.length) return { icon: '!', text: 'Account not ready', tone: 'warn' }
  if (!r) return { icon: '·', text: 'No video', tone: 'warn' }
  return { icon: '✓', text: 'Ready', tone: 'ok' }
}

export function IssueList({ issues, hideAutoFixes = false }: { issues: Issue[]; hideAutoFixes?: boolean }) {
  const shown = issues.filter((i) => !(hideAutoFixes && i.code === 'AUTO_FIXED'))
  if (shown.length === 0) return null
  const order = { error: 0, warning: 1, info: 2 }
  return (
    <ul className="issues">
      {[...shown]
        .sort((a, b) => order[a.severity] - order[b.severity])
        .map((i, n) => (
          <li key={n} className={`issue issue-${i.severity}`}>
            <span className="issue-icon">{i.severity === 'error' ? '✗' : i.severity === 'warning' ? (i.autoFix ? '⚙' : '!') : 'i'}</span>
            <span>{i.autoFix ? <>Auto-fixed: {i.autoFix}</> : i.message}</span>
          </li>
        ))}
    </ul>
  )
}

/** Renders a form from a platform's JSON options schema. */
export function OptionsForm({
  schema,
  values,
  disabled,
  onChange,
}: {
  schema: { properties: Record<string, SchemaProp> }
  values: Record<string, unknown>
  disabled?: boolean
  onChange: (key: string, value: unknown) => void
}) {
  return (
    <>
      {Object.entries(schema.properties).map(([key, prop]) => {
        if (prop.type === 'boolean') {
          return (
            <label key={key}>
              <input type="checkbox" role="switch" disabled={disabled} checked={Boolean(values[key])} onChange={(e) => onChange(key, e.target.checked)} />
              {prop.title ?? key}
              {prop.description && <small className="hint">{prop.description}</small>}
            </label>
          )
        }
        if (prop.enum) {
          const v = (values[key] as string) ?? ''
          return (
            <label key={key}>
              {prop.title ?? key}
              <select disabled={disabled} value={v} onChange={(e) => onChange(key, e.target.value)}>
                {prop.default === undefined && <option value="">— choose —</option>}
                {prop.enum.map((o) => (
                  <option key={o} value={o}>
                    {prop['x-enumLabels']?.[o] ?? o}
                  </option>
                ))}
              </select>
              {prop.description && <small className="hint">{prop.description}</small>}
            </label>
          )
        }
        return (
          <label key={key}>
            {prop.title ?? key}
            <input disabled={disabled} value={(values[key] as string) ?? ''} onChange={(e) => onChange(key, e.target.value)} />
          </label>
        )
      })}
    </>
  )
}

export function CopyButton({ text, label = 'Copy' }: { text: string; label?: string }) {
  const [done, setDone] = useState(false)
  return (
    <button
      type="button"
      className="secondary outline small"
      onClick={async () => {
        await navigator.clipboard.writeText(text)
        setDone(true)
        setTimeout(() => setDone(false), 1500)
      }}
    >
      {done ? 'Copied ✓' : label}
    </button>
  )
}

export function ErrorBanner({ error, onClose }: { error: string | null; onClose?: () => void }) {
  if (!error) return null
  return (
    <div className="banner banner-error" role="alert">
      <span>{error}</span>
      {onClose && (
        <button type="button" className="close" aria-label="Dismiss" onClick={onClose}>
          ×
        </button>
      )}
    </div>
  )
}
