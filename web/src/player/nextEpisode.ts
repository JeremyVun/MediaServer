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
    Mirrors `library.NextEpisode`, including its numbers for an episode
    without them (season 1, episode 0). */
export function nextEpisode(show: Pick<ShowDetail, 'seasons'>, currentItemID: number): LibraryEpisode | null {
  const episodes = show.seasons
    .flatMap((season) => season.episodes)
    .filter((entry): entry is LibraryEpisode => entry.kind === 'item')
    .map(numbered)
  const current = episodes.find((n) => n.item.id === currentItemID)
  if (!current) return null
  const special = current.season === 0
  const candidates = episodes
    .filter((n) => (n.season === 0) === special && n.item.available && follows(n, current))
    .sort((a, b) => a.season - b.season || a.episode - b.episode || a.item.id - b.item.id)
  return candidates[0]?.item ?? null
}

type Numbered = { item: LibraryEpisode; season: number; episode: number; last: number }

function numbered(item: LibraryEpisode): Numbered {
  const season = item.season ?? 1
  const episode = item.episode ?? 0
  return { item, season, episode, last: Math.max(episode, item.episode_end ?? 0) }
}

function follows(n: Numbered, current: Numbered): boolean {
  if (n.season !== current.season) return n.season > current.season
  return n.episode > current.last
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
