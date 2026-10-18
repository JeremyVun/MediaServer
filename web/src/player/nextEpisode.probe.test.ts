import { describe, expect, it } from 'vitest'
import type { ShowSeason } from '../api/types.ts'
import { nextEpisode, type LibraryEpisode } from './nextEpisode.ts'
import table from './testdata/next-episode-cases.json'

type Row = [id: number, season: number | null, episode: number | null, episodeEnd: number | null, available: boolean]

function item([id, season, episode, episodeEnd, available]: Row): LibraryEpisode {
  return {
    kind: 'item',
    id,
    type: 'episode',
    title: `Item ${id}`,
    year: null,
    duration_s: 1500,
    created_at: '2026-01-01 00:00:00',
    thumb_url: `/api/items/${id}/thumb?v=1`,
    available,
    collection_ids: [],
    show: { id: 1, title: 'Show' },
    season,
    episode,
    episode_end: episodeEnd,
    needs_check: false,
    tmdb_id: null,
    art_url: null,
  }
}

// The wire shape of GET /api/shows/{id}: ListShowItems order, grouped by
// COALESCE(season, 1), specials last (httpapi/shows.go seasons()).
function wireSeasons(rows: Row[]): ShowSeason[] {
  const ordered = [...rows].sort((a, b) => {
    const sa = a[1] ?? 1
    const sb = b[1] ?? 1
    return Number(sa === 0) - Number(sb === 0) || sa - sb || (a[2] ?? 0) - (b[2] ?? 0) || a[0] - b[0]
  })
  const bySeason = new Map<number, LibraryEpisode[]>()
  for (const row of ordered) {
    const season = row[1] ?? 1
    bySeason.set(season, [...(bySeason.get(season) ?? []), item(row)])
  }
  return [...bySeason.entries()].map(([season, episodes]) => ({ season, episodes }))
}

describe('probe: NextEpisode client agrees with library.NextEpisode', () => {
  // probe: the client's autoplay target equals the server's NextEpisode on the same show
  it.each(table.cases.map((c) => [c.name, c] as const))('%s', (_name, c) => {
    const show = { seasons: wireSeasons(c.episodes as Row[]) }
    expect(nextEpisode(show, c.current)?.id ?? null).toBe(c.expected)
  })
})
