import type { ItemSummary, PlayMarkers, ShowDetail } from '../api/types.ts'

export const COUNTDOWN_MS = 10_000
/** With no credits marker, the card shows this long before the end. */
export const LEAD_WITHOUT_CREDITS_S = 20

export type LibraryEpisode = { kind: 'item' } & ItemSummary

/** Where the Next card appears: the credits marker, else 20 s before the
    end. Null until the duration is known. */
export function cardStartS(durationS: number, markers: PlayMarkers | undefined): number | null {
  if (!(durationS > 0)) return null
  const credits = markers?.credits_start_s
  if (credits != null && credits > 0 && credits < durationS) return credits
  return Math.max(0, durationS - LEAD_WITHOUT_CREDITS_S)
}

export function pastCardStart(positionS: number, durationS: number, markers: PlayMarkers | undefined): boolean {
  const start = cardStartS(durationS, markers)
  return start != null && positionS >= start
}

export type CardMode = 'hidden' | 'countdown' | 'static'

/** A dismissed card stays away until `ended`, which brings it back without
    a countdown. */
export function cardMode(input: { pastStart: boolean; ended: boolean; dismissed: boolean }): CardMode {
  if (input.ended) return input.dismissed ? 'static' : 'countdown'
  return input.pastStart && !input.dismissed ? 'countdown' : 'hidden'
}

export function countdownDone(elapsedMs: number): boolean {
  return elapsedMs >= COUNTDOWN_MS
}

export function countdownFraction(elapsedMs: number): number {
  return Math.max(0, Math.min(1, elapsedMs / COUNTDOWN_MS))
}

/** The next episode is looked up once playback passes halfway. */
export function wantsNextEpisode(positionS: number, durationS: number): boolean {
  return durationS > 0 && positionS >= durationS / 2
}

export function inIntro(positionS: number, markers: PlayMarkers | undefined): boolean {
  const intro = markers?.intro
  return intro != null && positionS >= intro.start_s && positionS < intro.end_s
}

/** Whether a progress write carries `completed: true`. */
export function progressCompleted(input: {
  positionS: number
  creditsStartS: number | undefined
  advanced: boolean
}): boolean {
  if (input.advanced) return true
  return input.creditsStartS != null && input.positionS >= input.creditsStartS
}

/** What autoplay plays after `currentItemID`: the next available episode in
    the same sequence (specials or main), skipping gaps and offline files.
    Mirrors `library.NextEpisode`. */
export function nextEpisode(show: Pick<ShowDetail, 'seasons'>, currentItemID: number): LibraryEpisode | null {
  const episodes = show.seasons.flatMap((season) => season.episodes).filter(isNumberedEpisode)
  const current = episodes.find((episode) => episode.id === currentItemID)
  if (!current) return null
  const special = current.season === 0
  const candidates = episodes
    .filter((episode) => (episode.season === 0) === special && episode.available && follows(episode, current))
    .sort((a, b) => a.season - b.season || a.episode - b.episode)
  return candidates[0] ?? null
}

type NumberedEpisode = LibraryEpisode & { season: number; episode: number }

function isNumberedEpisode(entry: ShowDetail['seasons'][number]['episodes'][number]): entry is NumberedEpisode {
  return entry.kind === 'item' && entry.season != null && entry.episode != null
}

function follows(episode: NumberedEpisode, current: NumberedEpisode): boolean {
  if (episode.season !== current.season) return episode.season > current.season
  return episode.episode > Math.max(current.episode, current.episode_end ?? 0)
}

/** Rows of baked-in letterbox at the top and bottom of a still, as fractions
    of its height, from per-row mean luma (0–255). A frame that is dark
    throughout keeps every row. */
export function letterboxBars(rowLuma: number[], threshold = 18): { top: number; bottom: number } {
  const rows = rowLuma.length
  let top = 0
  while (top < rows && rowLuma[top] < threshold) top++
  let bottom = 0
  while (bottom < rows - top && rowLuma[rows - 1 - bottom] < threshold) bottom++
  if (rows === 0 || rows - top - bottom < rows / 4) return { top: 0, bottom: 0 }
  return { top: top / rows, bottom: bottom / rows }
}
