import { describe, expect, it } from 'vitest'
import type { ShowEpisodeEntry, ShowSeason, UpNext } from '../../api/types.ts'
import { compareSeasons, initialSeason, seasonAccessibleName, seasonLabel, seasonOptions } from './seasons.ts'

const present = (season: number, n: number): ShowEpisodeEntry =>
  ({ kind: 'item', id: season * 100 + n, season, episode: n }) as ShowEpisodeEntry
const missing = (season: number, n: number): ShowEpisodeEntry => ({
  kind: 'missing',
  season,
  number: n,
  title: `Episode ${n}`,
  airdate: null,
  missing: true,
})
const season = (n: number, ...episodes: ShowEpisodeEntry[]): ShowSeason => ({ season: n, episodes })
const upNext = (s: number): UpNext => ({
  item_id: 1,
  season: s,
  episode: 1,
  episode_end: null,
  title: 'x',
  available: true,
  thumb_url: '',
})

describe('compareSeasons', () => {
  it.each([
    [[3, 1, 0, 2], [1, 2, 3, 0]],
    [[0, 10, 9], [9, 10, 0]],
    [[0], [0]],
  ])('orders %j as %j', (input, want) => {
    expect([...input].sort(compareSeasons)).toEqual(want)
  })
})

describe('seasonOptions', () => {
  it('puts specials last and flags wholly missing seasons', () => {
    const got = seasonOptions([
      season(0, missing(0, 1)),
      season(2, present(2, 1), missing(2, 2)),
      season(1, missing(1, 1), missing(1, 2)),
    ])
    expect(got).toEqual([
      { season: 1, missing: true },
      { season: 2, missing: false },
      { season: 0, missing: true },
    ])
  })

  it('treats an empty season as missing', () => {
    expect(seasonOptions([season(1)])).toEqual([{ season: 1, missing: true }])
  })
})

describe('initialSeason', () => {
  const twentyTwo = Array.from({ length: 22 }, (_, i) =>
    season(i + 1, i < 3 ? present(i + 1, 1) : missing(i + 1, 1)),
  )
  it.each<[string, { up_next: UpNext | null; seasons: ShowSeason[] }, number | null]>([
    ['no seasons', { up_next: null, seasons: [] }, null],
    ['up-next season wins', { up_next: upNext(2), seasons: [season(1, present(1, 1)), season(2, missing(2, 1)), season(3, present(3, 1))] }, 2],
    ['caught up: latest with library episodes', { up_next: null, seasons: [season(1, present(1, 1)), season(2, present(2, 1)), season(3, missing(3, 1))] }, 2],
    ['specials only as a last resort', { up_next: null, seasons: [season(1, missing(1, 1)), season(0, present(0, 1))] }, 0],
    ['main season beats specials', { up_next: null, seasons: [season(1, present(1, 1)), season(0, present(0, 1))] }, 1],
    ['nothing present: first season', { up_next: null, seasons: [season(2, missing(2, 1)), season(1, missing(1, 1))] }, 1],
    ['up-next season absent from the list falls through', { up_next: upNext(9), seasons: [season(1, present(1, 1))] }, 1],
    ['22 seasons, up next deep in the row', { up_next: upNext(21), seasons: twentyTwo }, 21],
    ['22 seasons, caught up', { up_next: null, seasons: twentyTwo }, 3],
  ])('%s', (_, show, want) => {
    expect(initialSeason(show)).toBe(want)
  })
})

describe('labels', () => {
  it.each<[number, boolean, string]>([
    [1, false, 'Season 1'],
    [12, true, 'S12'],
    [0, true, 'Specials'],
    [0, false, 'Specials'],
  ])('season %i short=%s → %s', (n, short, want) => {
    expect(seasonLabel(n, short)).toBe(want)
  })

  it('names a missing season for screen readers', () => {
    expect(seasonAccessibleName({ season: 3, missing: true })).toBe('Season 3, none in your library')
    expect(seasonAccessibleName({ season: 0, missing: false })).toBe('Specials')
  })
})
