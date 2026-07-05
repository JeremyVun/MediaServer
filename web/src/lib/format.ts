export function formatClock(seconds: number | null | undefined): string {
  if (!Number.isFinite(seconds ?? NaN) || seconds == null || seconds < 0) return '0:00'
  const whole = Math.floor(seconds)
  const h = Math.floor(whole / 3600)
  const m = Math.floor((whole % 3600) / 60)
  const s = whole % 60
  if (h > 0) return `${h}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`
  return `${m}:${String(s).padStart(2, '0')}`
}

export function formatDuration(seconds: number | null | undefined): string {
  if (!Number.isFinite(seconds ?? NaN) || seconds == null || seconds <= 0) return 'Unknown length'
  const minutes = Math.round(seconds / 60)
  if (minutes < 60) return `${minutes} min`
  const h = Math.floor(minutes / 60)
  const m = minutes % 60
  return m === 0 ? `${h} hr` : `${h} hr ${m} min`
}

export function formatBytes(bytes: number | null | undefined): string {
  if (!Number.isFinite(bytes ?? NaN) || bytes == null || bytes < 0) return 'Unknown size'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit += 1
  }
  const fixed = value >= 10 || unit === 0 ? value.toFixed(0) : value.toFixed(1)
  return `${fixed} ${units[unit]}`
}

export function progressPercent(position: number | undefined, duration: number | null | undefined): number {
  if (!position || !duration || duration <= 0) return 0
  return Math.max(0, Math.min(100, (position / duration) * 100))
}

const NEW_ITEM_WINDOW_MS = 24 * 60 * 60 * 1000

/**
 * SQLite text timestamps are "YYYY-MM-DD HH:MM:SS" UTC with no timezone
 * marker, so they must be coerced to ISO 8601 before `Date` parsing.
 */
export function parseServerTimestamp(value: string): number {
  return new Date(`${value.replace(' ', 'T')}Z`).getTime()
}

export function isRecentlyCreated(createdAt: string | null | undefined, now = Date.now()): boolean {
  if (!createdAt) return false
  const created = parseServerTimestamp(createdAt)
  if (!Number.isFinite(created)) return false
  return now - created < NEW_ITEM_WINDOW_MS
}
