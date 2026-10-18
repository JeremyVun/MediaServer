import { useQuery } from '@tanstack/react-query'
import type { ShowSeason } from '../../api/types.ts'

/** A candidate's season numbers, asked of TVmaze directly: the server's
    match-search carries none (one request per candidate, M13), and this is
    one CORS request per pick, only while the Change dialog is open. On
    failure the season row stays hidden and the server keeps its offset. */
export function useTvmazeSeasons(tvmazeID: number | null) {
  return useQuery({
    queryKey: ['tvmaze-seasons', tvmazeID],
    queryFn: async ({ signal }) => {
      const res = await fetch(`https://api.tvmaze.com/shows/${tvmazeID}/seasons`, { signal })
      if (!res.ok) throw new Error(`TVmaze ${res.status}`)
      const body = (await res.json()) as { number: number | null }[]
      return body.map((s) => s.number).filter((n): n is number => n != null && n > 0)
    },
    enabled: tvmazeID != null,
    staleTime: Infinity,
    retry: false,
  })
}

/** Seasons the library holds episodes of, in file numbering, specials aside. */
export function fileSeasons(seasons: ShowSeason[]): number[] {
  return seasons
    .filter((s) => s.season > 0 && s.episodes.some((e) => e.kind === 'item'))
    .map((s) => s.season)
    .sort((a, b) => a - b)
}

/** The first file season the TVmaze show doesn't have, else null. */
export function seasonGap(files: number[], tvmaze: number[]): number | null {
  if (tvmaze.length === 0) return null
  return files.find((s) => !tvmaze.includes(s)) ?? null
}

/** The TVmaze season the files' season most likely is: the current offset's
    pick when it still exists, else the latest TVmaze season before it, else
    the first. */
export function defaultTvmazeSeason(fileSeason: number, tvmaze: number[], currentOffset: number): number {
  const sorted = [...tvmaze].sort((a, b) => a - b)
  const current = fileSeason + currentOffset
  if (currentOffset !== 0 && sorted.includes(current)) return current
  return sorted.filter((s) => s < fileSeason).at(-1) ?? sorted[0]
}
