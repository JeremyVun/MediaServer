import type { ItemSummary, MissingEpisode, ShowEpisodeEntry, ShowSeason } from '../../api/types.ts'
import { formatDuration } from '../../lib/format.ts'

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']
const DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat']

export type ItemEpisode = { kind: 'item' } & ItemSummary

/** Local calendar date as `YYYY-MM-DD`, comparable with TVmaze airdates. */
export function localISODate(now: Date = new Date()): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}`
}

export function isUnaired(episode: MissingEpisode, today: string): boolean {
  return episode.airdate != null && episode.airdate > today
}

function parts(date: string) {
  const [y, m, d] = date.split('-').map(Number)
  return { y, m, d }
}

/** `3 Aug` in the current year, `May 2024` otherwise. */
export function formatAired(date: string, today: string): string {
  const { y, m, d } = parts(date)
  return y === parts(today).y ? `${d} ${MONTHS[m - 1]}` : `${MONTHS[m - 1]} ${y}`
}

/** `Fri 23 Oct`, with the year outside the current year. */
export function formatAirs(date: string, today: string): string {
  const { y, m, d } = parts(date)
  const weekday = DAYS[new Date(Date.UTC(y, m - 1, d)).getUTCDay()]
  return `${weekday} ${d} ${MONTHS[m - 1]}${y === parts(today).y ? '' : ` ${y}`}`
}

/** The missing or unaired line under an imageless tile's title. */
export function missingLine(episode: MissingEpisode, today: string): { lead: string; rest: string } {
  if (isUnaired(episode, today)) return { lead: '', rest: `Airs ${formatAirs(episode.airdate!, today)}` }
  return { lead: 'Missing', rest: episode.airdate ? ` · Aired ${formatAired(episode.airdate, today)}` : '' }
}

/** `1`, `1–2` for a multi-episode file; specials carry no number. */
export function episodeNumber(season: number, episode: number | null, end: number | null): string {
  if (season === 0 || episode == null) return ''
  return end ? `${episode}–${end}` : String(episode)
}

/** Duration and watch state for a present episode's foot: `53 min · Watched`. */
export function episodeState(item: ItemEpisode): string[] {
  const p = item.progress
  const out = [formatDuration(item.duration_s)]
  if (p?.completed) out.push('Watched')
  else if (p && p.position_s > 30 && item.available && item.duration_s) {
    out.push(`${formatDuration(item.duration_s - p.position_s)} left`)
  }
  return out
}

export function episodeProgress(item: ItemEpisode): number {
  const p = item.progress
  if (!p) return 0
  if (p.completed) return 100
  if (!item.duration_s) return 0
  return Math.max(0, Math.min(100, (p.position_s / item.duration_s) * 100))
}

export function isWhollyMissing(season: ShowSeason): boolean {
  return season.episodes.every((e) => e.kind === 'missing')
}

/** Copy for the one tile a wholly missing season folds into. */
export function foldSummary(episodes: MissingEpisode[], today: string): { title: string; meta: string } {
  const unaired = episodes.filter((e) => isUnaired(e, today))
  const aired = episodes.length - unaired.length
  const nextAir = unaired.map((e) => e.airdate!).sort()[0]
  if (aired === 0) {
    const count = `${episodes.length} ${episodes.length === 1 ? 'episode' : 'episodes'}`
    return { title: count, meta: nextAir ? `Airs ${formatAirs(nextAir, today)}` : '' }
  }
  if (unaired.length > 0) return { title: `${aired} missing`, meta: `Next airs ${formatAirs(nextAir, today)}` }
  const first = episodes.map((e) => e.airdate).filter((d): d is string => d != null).sort()[0]
  return { title: aired === 1 ? '1 missing' : `All ${aired} missing`, meta: first ? `Aired ${formatAired(first, today)}` : '' }
}

/** First library episode in episode order, specials last. */
export function firstPlayable(seasons: ShowSeason[]): ItemEpisode | null {
  const ordered = [...seasons].sort((a, b) => (a.season === 0 ? 1 : b.season === 0 ? -1 : a.season - b.season))
  for (const season of ordered) {
    const hit = season.episodes.find((e: ShowEpisodeEntry): e is ItemEpisode => e.kind === 'item')
    if (hit) return hit
  }
  return null
}
