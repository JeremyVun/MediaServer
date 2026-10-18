import { describe, expect, it } from 'vitest'
import type { MissingEpisode } from '../../api/types.ts'
import { episodeNumber, foldSummary, formatAired, formatAirs, localISODate, missingLine } from './episodeFormat.ts'

const TODAY = '2026-10-19'
const missing = (number: number, airdate: string | null): MissingEpisode => ({
  kind: 'missing',
  season: 1,
  number,
  title: `Episode ${number}`,
  airdate,
  missing: true,
})

describe('dates', () => {
  it.each([
    ['2026-08-03', '3 Aug'],
    ['2024-05-15', 'May 2024'],
  ])('aired %s → %s', (date, want) => expect(formatAired(date, TODAY)).toBe(want))

  it.each([
    ['2026-10-23', 'Fri 23 Oct'],
    ['2027-07-09', 'Fri 9 Jul 2027'],
  ])('airs %s → %s', (date, want) => expect(formatAirs(date, TODAY)).toBe(want))

  it('formats the local date', () => {
    expect(localISODate(new Date(2026, 0, 5))).toBe('2026-01-05')
  })
})

describe('missingLine', () => {
  it.each<[string | null, string, string]>([
    ['2026-08-03', 'Missing', ' · Aired 3 Aug'],
    [null, 'Missing', ''],
    ['2026-10-23', '', 'Airs Fri 23 Oct'],
    [TODAY, 'Missing', ' · Aired 19 Oct'],
  ])('%s', (airdate, lead, rest) => {
    expect(missingLine(missing(1, airdate), TODAY)).toEqual({ lead, rest })
  })
})

describe('foldSummary', () => {
  it.each<[string, MissingEpisode[], { title: string; meta: string }]>([
    ['all aired', [missing(2, '2024-05-15'), missing(1, '2024-05-08')], { title: 'All 2 missing', meta: 'Aired May 2024' }],
    ['all unaired', [missing(1, '2027-07-09'), missing(2, '2027-07-16')], { title: '2 episodes', meta: 'Airs Fri 9 Jul 2027' }],
    ['mixed', [missing(1, '2026-10-01'), missing(2, '2026-10-30'), missing(3, '2026-10-23')], { title: '1 missing', meta: 'Next airs Fri 23 Oct' }],
    ['no dates', [missing(1, null)], { title: '1 missing', meta: '' }],
  ])('%s', (_, episodes, want) => expect(foldSummary(episodes, TODAY)).toEqual(want))
})

describe('episodeNumber', () => {
  it.each<[number, number | null, number | null, string]>([
    [1, 3, null, '3'],
    [2, 1, 2, '1–2'],
    [0, 2, null, ''],
  ])('S%i E%s end %s → %s', (season, episode, end, want) => {
    expect(episodeNumber(season, episode, end)).toBe(want)
  })
})
