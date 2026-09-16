import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Quality, QualityRung } from '../api/types.ts'
import {
  nearestRung,
  qualityAfterItemChange,
  readStoredQuality,
  resolveQualityLabel,
  writeStoredQuality,
} from './quality.ts'

// vitest runs in the node environment, so the tests supply the storage.
function stubStorage(stored?: string): Map<string, string> {
  const entries = new Map<string, string>()
  if (stored !== undefined) entries.set('playerQuality', stored)
  vi.stubGlobal('localStorage', {
    getItem: (key: string) => entries.get(key) ?? null,
    setItem: (key: string, value: string) => entries.set(key, value),
  })
  return entries
}

afterEach(() => vi.unstubAllGlobals())

const LADDER: QualityRung[] = [
  { id: '1080p', width: 1920, height: 1080 },
  { id: '720p', width: 1280, height: 720 },
  { id: '480p', width: 854, height: 480 },
  { id: '360p', width: 640, height: 360 },
]

describe('readStoredQuality', () => {
  const cases: Array<[string, string | undefined, Quality]> = [
    ['nothing stored', undefined, 'original'],
    ['auto', 'auto', 'auto'],
    ['original', 'original', 'original'],
    ['1080p', '1080p', '1080p'],
    ['720p', '720p', '720p'],
    ['480p', '480p', '480p'],
    ['360p', '360p', '360p'],
    ['empty string', '', 'original'],
    ['unknown value', 'ultra', 'original'],
    ['wrong case', '1080P', 'original'],
    ['dropped rung', '240p', 'original'],
    ['audio', 'audio', 'original'],
    ['object prototype key', 'toString', 'original'],
  ]
  it.each(cases)('%s reads as %s', (_name, stored, expected) => {
    stubStorage(stored)
    expect(readStoredQuality()).toBe(expected)
  })
})

describe('writeStoredQuality', () => {
  it.each<Quality>(['auto', 'original', '1080p', '720p', '480p', '360p'])('round-trips %s', (quality) => {
    const entries = stubStorage()
    writeStoredQuality(quality)
    expect(entries.get('playerQuality')).toBe(quality)
    expect(readStoredQuality()).toBe(quality)
  })

  it('replaces a previous pick', () => {
    const entries = stubStorage('1080p')
    writeStoredQuality('auto')
    expect(entries.get('playerQuality')).toBe('auto')
  })
})

describe('nearestRung', () => {
  const cases: Array<[string, QualityRung[], number, string | null]> = [
    ['exact 1080p', LADDER, 1080, '1080p'],
    ['exact 720p', LADDER, 720, '720p'],
    ['exact 360p', LADDER, 360, '360p'],
    ['between rungs, nearer 480p', LADDER, 500, '480p'],
    ['between rungs, nearer 720p', LADDER, 700, '720p'],
    ['tie keeps the larger rung', LADDER, 600, '720p'],
    ['letterboxed 1080p output', LADDER, 800, '720p'],
    ['taller than every rung', LADDER, 2160, '1080p'],
    ['shorter than every rung', LADDER, 120, '360p'],
    ['portrait rung heights', [{ id: '720p', width: 720, height: 1280 }], 1280, '720p'],
    ['no rungs offered', [], 720, null],
    ['height unknown before metadata', LADDER, 0, null],
    ['NaN height', LADDER, NaN, null],
  ]
  it.each(cases)('%s', (_name, qualities, videoHeight, expected) => {
    expect(nearestRung(qualities, videoHeight)?.id ?? null).toBe(expected)
  })
})

describe('qualityAfterItemChange', () => {
  const cases: Array<[string, Quality, Quality, Quality]> = [
    ['audio falls back to the stored preference', 'audio', '720p', '720p'],
    ['audio falls back to a stored auto', 'audio', 'auto', 'auto'],
    ['audio falls back to the default', 'audio', 'original', 'original'],
    ['auto is kept', 'auto', '720p', 'auto'],
    ['original is kept', 'original', '1080p', 'original'],
    ['a fixed rung is kept', '480p', 'original', '480p'],
    ['a fixed rung is kept over a differing store', '1080p', '360p', '1080p'],
  ]
  it.each(cases)('%s', (_name, requested, stored, expected) => {
    expect(qualityAfterItemChange(requested, stored)).toBe(expected)
  })
})

describe('resolveQualityLabel', () => {
  const cases: Array<[string, Quality, QualityRung[], number, string]> = [
    ['auto names the size playing now', 'auto', LADDER, 720, 'Auto (720p)'],
    ['auto after a step down', 'auto', LADDER, 360, 'Auto (360p)'],
    ['auto before the first frame', 'auto', LADDER, 0, 'Auto'],
    ['auto with no rungs', 'auto', [], 720, 'Auto'],
    ['original ignores the rungs', 'original', LADDER, 1080, 'Original'],
    ['fixed 1080p', '1080p', LADDER, 1080, '1080p'],
    ['fixed 480p resolved down', '480p', LADDER, 1080, '480p'],
    ['audio only', 'audio', LADDER, 0, 'Audio only'],
    ['audio only ignores a stale frame height', 'audio', LADDER, 1080, 'Audio only'],
  ]
  it.each(cases)('%s', (_name, resolved, qualities, playingHeight, expected) => {
    expect(resolveQualityLabel(resolved, qualities, playingHeight)).toBe(expected)
  })
})
