import type { ShowDetail, ShowSeason } from '../../api/types.ts'

export interface SeasonOption {
  season: number
  /** No episode of this season is in the library. */
  missing: boolean
}

/** Past this many chips, labels shorten to `S3` so the row stays scannable. */
export const SHORT_LABEL_THRESHOLD = 4

/** Ascending, with specials (season 0) last. */
export function compareSeasons(a: number, b: number): number {
  if (a === b) return 0
  if (a === 0) return 1
  if (b === 0) return -1
  return a - b
}

export function seasonOptions(seasons: ShowSeason[]): SeasonOption[] {
  return seasons
    .map((s) => ({ season: s.season, missing: s.episodes.every((e) => e.kind === 'missing') }))
    .sort((a, b) => compareSeasons(a.season, b.season))
}

/** The up-next season, else the latest season with library episodes
    (specials only when nothing else has any), else the first season. */
export function initialSeason(show: Pick<ShowDetail, 'up_next' | 'seasons'>): number | null {
  const options = seasonOptions(show.seasons)
  if (options.length === 0) return null
  const upNext = show.up_next?.season
  if (upNext != null && options.some((o) => o.season === upNext)) return upNext
  const present = options.filter((o) => !o.missing)
  const main = present.filter((o) => o.season !== 0)
  const pick = main.at(-1) ?? present.at(-1) ?? options[0]
  return pick.season
}

export function seasonLabel(season: number, short: boolean): string {
  if (season === 0) return 'Specials'
  return short ? `S${season}` : `Season ${season}`
}

export function seasonAccessibleName(option: SeasonOption): string {
  const name = seasonLabel(option.season, false)
  return option.missing ? `${name}, none in your library` : name
}
