import { describe, expect, it } from 'vitest'
import { rankShows } from './rankShows.ts'

const shows = ['Andor', 'Below', 'Star Trek Strange New Worlds', 'The Terminal List'].map((title) => ({ title }))
const titles = (list: { title: string }[]) => list.map((s) => s.title)

describe('rankShows', () => {
  it('lists shows sharing a word with the source first', () => {
    expect(titles(rankShows(shows, '', 'Star Wars Andor'))).toEqual([
      'Andor',
      'Star Trek Strange New Worlds',
      'Below',
      'The Terminal List',
    ])
  })

  it('filters by the search text', () => {
    expect(titles(rankShows(shows, 'ter', 'x'))).toEqual(['The Terminal List'])
  })

  it('ignores one-letter words', () => {
    expect(titles(rankShows(shows, '', 'A B'))).toEqual(titles(shows))
  })
})
