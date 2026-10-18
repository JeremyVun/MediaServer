import { describe, expect, it } from 'vitest'
import type { ShowSeason } from '../../api/types.ts'
import { defaultTvmazeSeason, fileSeasons, seasonGap } from './tvmazeSeasons.ts'

describe('fileSeasons', () => {
  it('keeps seasons with library episodes, specials aside', () => {
    const seasons = [
      { season: 1, episodes: [{ kind: 'missing' }] },
      { season: 2, episodes: [{ kind: 'item' }] },
      { season: 0, episodes: [{ kind: 'item' }] },
    ] as unknown as ShowSeason[]
    expect(fileSeasons(seasons)).toEqual([2])
  })
})

describe('seasonGap', () => {
  it.each<[string, number[], number[], number | null]>([
    ['Monsters: files say 2, TVmaze has 1', [2], [1], 2],
    ['covered', [1, 2], [1, 2, 3], null],
    ['seasons unknown', [2], [], null],
    ['first gap wins', [1, 3, 4], [1, 2], 3],
  ])('%s', (_, files, tvmaze, want) => expect(seasonGap(files, tvmaze)).toBe(want))
})

describe('defaultTvmazeSeason', () => {
  it.each<[string, number, number[], number, number]>([
    ['latest before the files season', 2, [1], 0, 1],
    ['nothing before: first', 1, [3, 2], 0, 2],
    ['current offset kept', 3, [1, 2], -1, 2],
    ['stale offset falls back', 3, [1, 2], -5, 2],
  ])('%s', (_, file, tvmaze, offset, want) => expect(defaultTvmazeSeason(file, tvmaze, offset)).toBe(want))
})
