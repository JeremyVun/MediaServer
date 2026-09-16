import type { Quality, QualityRung } from '../api/types.ts'

export type { Quality }

// Per-device quality preference and the Quality menu's labels. Kept free of
// React so it can be table-tested (see quality.test.ts).

const STORAGE_KEY = 'playerQuality'
const DEFAULT_QUALITY: Quality = 'original'
// No 'audio': Audio only is never remembered, so a stored one reads as the default.
const QUALITY_VALUES: Quality[] = ['auto', 'original', '1080p', '720p', '480p', '360p']
const VALID = new Set<string>(QUALITY_VALUES)

export function readStoredQuality(): Quality {
  const stored = localStorage.getItem(STORAGE_KEY)
  return stored != null && VALID.has(stored) ? (stored as Quality) : DEFAULT_QUALITY
}

export function writeStoredQuality(quality: Quality): void {
  localStorage.setItem(STORAGE_KEY, quality)
}

/**
 * The requested quality to carry into another item. Audio only is a per-listen
 * pick that is never stored, so it falls back to the remembered preference.
 */
export function qualityAfterItemChange(requested: Quality, stored: Quality): Quality {
  return requested === 'audio' ? stored : requested
}

/** The offered rung closest in height to the frame playing now, or null. */
export function nearestRung(qualities: QualityRung[], videoHeight: number): QualityRung | null {
  if (!Number.isFinite(videoHeight) || videoHeight <= 0) return null
  let closest: QualityRung | null = null
  for (const rung of qualities) {
    // Strict `<` breaks ties toward the larger rung: the list is largest first.
    if (closest == null || Math.abs(rung.height - videoHeight) < Math.abs(closest.height - videoHeight)) {
      closest = rung
    }
  }
  return closest
}

/** Menu text for the resolved quality: `Auto (720p)`, `Original`, `1080p`. */
export function resolveQualityLabel(
  resolved: Quality,
  qualities: QualityRung[],
  playingHeight: number,
): string {
  if (resolved === 'original') return 'Original'
  if (resolved === 'audio') return 'Audio only'
  if (resolved !== 'auto') return resolved
  const rung = nearestRung(qualities, playingHeight)
  return rung ? `Auto (${rung.id})` : 'Auto'
}
