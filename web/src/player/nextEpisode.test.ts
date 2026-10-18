import { describe, expect, it } from 'vitest'
import type { MissingEpisode, PlayMarkers, ShowSeason } from '../api/types.ts'
import {
  cardMode,
  cardStartS,
  countdownDone,
  countdownFraction,
  inIntro,
  letterboxBars,
  nextEpisode,
  pastCardStart,
  progressCompleted,
  wantsNextEpisode,
  type CardMode,
  type LibraryEpisode,
} from './nextEpisode.ts'

const credits: PlayMarkers = { credits_start_s: 3300 }
const intro: PlayMarkers = { intro: { start_s: 60, end_s: 120 } }

describe('cardStartS', () => {
  const cases: Array<[string, number, PlayMarkers | undefined, number | null]> = [
    ['credits marker', 3600, credits, 3300],
    ['no marker: 20 s before the end', 3600, {}, 3580],
    ['markers not loaded', 3600, undefined, 3580],
    ['a marker at the end is ignored', 3600, { credits_start_s: 3600 }, 3580],
    ['a zero marker is ignored', 3600, { credits_start_s: 0 }, 3580],
    ['shorter than the lead', 12, {}, 0],
    ['duration unknown', 0, credits, null],
    ['duration NaN', Number.NaN, credits, null],
  ]
  it.each(cases)('%s', (_name, duration, markers, expected) => {
    expect(cardStartS(duration, markers)).toBe(expected)
  })
})

describe('pastCardStart', () => {
  const cases: Array<[string, number, number, PlayMarkers | undefined, boolean]> = [
    ['before the credits', 3299, 3600, credits, false],
    ['at the credits', 3300, 3600, credits, true],
    ['no marker, 21 s left', 3579, 3600, {}, false],
    ['no marker, 20 s left', 3580, 3600, {}, true],
    ['duration unknown', 3580, 0, {}, false],
  ]
  it.each(cases)('%s', (_name, position, duration, markers, expected) => {
    expect(pastCardStart(position, duration, markers)).toBe(expected)
  })
})

describe('cardMode', () => {
  const cases: Array<[string, boolean, boolean, boolean, CardMode]> = [
    ['before the start', false, false, false, 'hidden'],
    ['past the start', true, false, false, 'countdown'],
    ['dismissed past the start', true, false, true, 'hidden'],
    ['ended without dismissal keeps counting', true, true, false, 'countdown'],
    ['ended after dismissal: static card', true, true, true, 'static'],
    ['ended after seeking straight to the end', false, true, false, 'countdown'],
  ]
  it.each(cases)('%s', (_name, pastStart, ended, dismissed, expected) => {
    expect(cardMode({ pastStart, ended, dismissed })).toBe(expected)
  })
})

describe('countdown', () => {
  const cases: Array<[number, boolean, number]> = [
    [0, false, 0],
    [2_500, false, 0.25],
    [9_999, false, 0.9999],
    [10_000, true, 1],
    [12_000, true, 1],
    [-5, false, 0],
  ]
  it.each(cases)('%i ms elapsed → done %s, bar %d', (elapsed, done, fraction) => {
    expect(countdownDone(elapsed)).toBe(done)
    expect(countdownFraction(elapsed)).toBeCloseTo(fraction)
  })
})

describe('wantsNextEpisode', () => {
  const cases: Array<[string, number, number, boolean]> = [
    ['first frame', 0, 3600, false],
    ['just under halfway', 1799.9, 3600, false],
    ['halfway', 1800, 3600, true],
    ['near the end', 3590, 3600, true],
    ['duration unknown', 100, 0, false],
  ]
  it.each(cases)('%s', (_name, position, duration, expected) => {
    expect(wantsNextEpisode(position, duration)).toBe(expected)
  })
})

describe('inIntro', () => {
  const cases: Array<[string, number, PlayMarkers | undefined, boolean]> = [
    ['before', 59.9, intro, false],
    ['at the start', 60, intro, true],
    ['inside', 90, intro, true],
    ['at the end', 120, intro, false],
    ['no intro', 90, credits, false],
    ['markers not loaded', 90, undefined, false],
  ]
  it.each(cases)('%s', (_name, position, markers, expected) => {
    expect(inIntro(position, markers)).toBe(expected)
  })
})

describe('progressCompleted', () => {
  const cases: Array<[string, number, number | undefined, boolean, boolean]> = [
    ['advanced from the card', 100, undefined, true, true],
    ['passed the credits', 3300, 3300, false, true],
    ['before the credits', 3299, 3300, false, false],
    ['no marker, near the end (the server applies 95 %)', 3590, undefined, false, false],
    ['rewatch from the start', 10, 3300, false, false],
  ]
  it.each(cases)('%s', (_name, positionS, creditsStartS, advanced, expected) => {
    expect(progressCompleted({ positionS, creditsStartS, advanced })).toBe(expected)
  })
})

function ep(id: number, season: number, episode: number, overrides: Partial<LibraryEpisode> = {}): LibraryEpisode {
  return {
    kind: 'item',
    id,
    type: 'episode',
    title: `S${season}E${episode}`,
    year: null,
    duration_s: 1500,
    created_at: '2026-01-01 00:00:00',
    thumb_url: `/api/items/${id}/thumb?v=1`,
    available: true,
    collection_ids: [],
    show: { id: 1, title: 'Show' },
    season,
    episode,
    episode_end: null,
    needs_check: false,
    ...overrides,
  }
}

function missing(season: number, number: number): MissingEpisode {
  return { kind: 'missing', season, number, title: 'Gone', airdate: '2026-01-01', missing: true }
}

const show = (...seasons: ShowSeason[]) => ({ seasons })

describe('nextEpisode', () => {
  const furious = show(
    { season: 1, episodes: [ep(11, 1, 1), ep(12, 1, 2), ep(13, 1, 3), missing(1, 4), ep(15, 1, 5), ep(16, 1, 6, { available: false }), ep(17, 1, 7)] },
    { season: 2, episodes: [ep(21, 2, 1, { episode_end: 2 }), ep(23, 2, 3)] },
    { season: 0, episodes: [ep(1, 0, 1), ep(2, 0, 2)] },
  )
  const cases: Array<[string, number, number | null]> = [
    ['the next number', 11, 12],
    ['skips a missing episode', 13, 15],
    ['skips an offline episode', 15, 17],
    ['crosses into the next season', 17, 21],
    ['after a two-episode file', 21, 23],
    ['the last main episode never runs into specials', 23, null],
    ['a special leads to the next special', 1, 2],
    ['the last special', 2, null],
    ['an item not in the show', 999, null],
  ]
  it.each(cases)('%s', (_name, current, expected) => {
    expect(nextEpisode(furious, current)?.id ?? null).toBe(expected)
  })

  it('orders by number, not by list position', () => {
    const shuffled = show({ season: 1, episodes: [ep(13, 1, 3), ep(11, 1, 1), ep(12, 1, 2)] })
    expect(nextEpisode(shuffled, 11)?.id).toBe(12)
  })

  it('reaches a later season past a wholly missing one', () => {
    const gap = show(
      { season: 1, episodes: [ep(11, 1, 1)] },
      { season: 2, episodes: [missing(2, 1), missing(2, 2)] },
      { season: 3, episodes: [ep(31, 3, 1)] },
    )
    expect(nextEpisode(gap, 11)?.id).toBe(31)
  })
})

describe('letterboxBars', () => {
  const rows = (top: number, content: number, bottom: number, luma = 90) => [
    ...Array<number>(top).fill(2),
    ...Array<number>(content).fill(luma),
    ...Array<number>(bottom).fill(3),
  ]
  const cases: Array<[string, number[], { top: number; bottom: number }]> = [
    ['no bars', rows(0, 36, 0), { top: 0, bottom: 0 }],
    ['2.39:1 inside 16:9', rows(5, 26, 5), { top: 5 / 36, bottom: 5 / 36 }],
    ['uneven bars', rows(6, 26, 4), { top: 6 / 36, bottom: 4 / 36 }],
    ['a dark frame keeps every row', rows(0, 36, 0, 5), { top: 0, bottom: 0 }],
    ['mostly dark: not a letterbox', rows(16, 4, 16), { top: 0, bottom: 0 }],
    ['empty', [], { top: 0, bottom: 0 }],
  ]
  it.each(cases)('%s', (_name, luma, expected) => {
    const bars = letterboxBars(luma)
    expect(bars.top).toBeCloseTo(expected.top)
    expect(bars.bottom).toBeCloseTo(expected.bottom)
  })
})
