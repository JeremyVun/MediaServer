import { useLayoutEffect, useRef, useState, type CSSProperties, type SyntheticEvent } from 'react'
import { Loader2 } from 'lucide-react'
import { usePlayerShow } from '../api/queries.ts'
import type { MissingEpisode, ShowDetail, ShowEpisodeEntry } from '../api/types.ts'
import { SeasonChips } from '../features/show/SeasonChips.tsx'
import { initialSeason, seasonOptions } from '../features/show/seasons.ts'
import { formatDuration, progressPercent } from '../lib/format.ts'
import { EPISODES_TRIGGER } from './episodesTrigger.ts'
import { thumbnailGenerated } from './mediaSession.ts'

const LIST_FADE =
  '[mask-image:linear-gradient(to_bottom,transparent_0,black_20px,black_calc(100%-20px),transparent_100%)]'

/** The player's Episodes popover (variant b): season chips over a list capped
    at five rows. It anchors to whichever trigger is showing — the phone's
    top-bar icon (opens below) or the desktop control (opens above the seek
    bar) — and never covers the seek bar. */
export default function EpisodesPopover({
  id,
  showID,
  currentItemID,
  onPick,
  onOpenChange,
}: {
  id: string
  showID: number
  currentItemID: number
  onPick: (itemID: number) => void
  onOpenChange: (open: boolean) => void
}) {
  const popRef = useRef<HTMLDivElement>(null)
  const listRef = useRef<HTMLDivElement>(null)
  const [open, setOpen] = useState(false)
  const [placement, setPlacement] = useState<CSSProperties>()
  const [pickedSeason, setPickedSeason] = useState<number | null>(null)
  const focusPlaying = useRef(false)
  const show = usePlayerShow(showID, true)

  const playingSeason = show.data ? seasonOf(show.data, currentItemID) : null
  const season = pickedSeason ?? playingSeason

  // Mounted by the first tap on a trigger, before the trigger's native
  // popover target existed; `source` makes that trigger its invoker, so a
  // second tap closes it rather than light-dismissing and reopening.
  useLayoutEffect(() => {
    const pop = popRef.current
    pop?.showPopover({ source: visibleTrigger(pop.parentElement) })
  }, [])

  useLayoutEffect(() => {
    if (!open) return
    const place = () => {
      const main = popRef.current?.parentElement
      const trigger = visibleTrigger(main)
      const seek = main?.querySelector('[data-seek]')
      if (!trigger || !seek) return
      const rect = trigger.getBoundingClientRect()
      const seekTop = seek.getBoundingClientRect().top
      const right = Math.max(8, window.innerWidth - rect.right)
      if (trigger.getAttribute(EPISODES_TRIGGER) === 'top') {
        const top = rect.bottom + 4
        setPlacement({ inset: 'auto', top, right, maxHeight: seekTop - 8 - top })
      } else {
        setPlacement({ inset: 'auto', bottom: window.innerHeight - seekTop + 8, right, maxHeight: seekTop - 16 })
      }
    }
    place()
    window.addEventListener('resize', place)
    return () => window.removeEventListener('resize', place)
  }, [open])

  // Opens with the playing episode centred in the list.
  useLayoutEffect(() => {
    const list = listRef.current
    if (!open || !list) return
    const playing = list.querySelector<HTMLElement>('[aria-current="true"]')
    list.scrollTop = playing ? playing.offsetTop - (list.clientHeight - playing.offsetHeight) / 2 : 0
    if (playing && focusPlaying.current) {
      focusPlaying.current = false
      playing.focus({ preventScroll: true })
    }
  }, [open, season, show.data])

  const onToggle = (event: SyntheticEvent<HTMLDivElement>) => {
    const isOpen = (event.nativeEvent as ToggleEvent).newState === 'open'
    setOpen(isOpen)
    if (isOpen) {
      setPickedSeason(null)
      focusPlaying.current = true
    }
    onOpenChange(isOpen)
  }

  const options = show.data ? seasonOptions(show.data.seasons) : []
  const entries = show.data?.seasons.find((s) => s.season === season)?.episodes ?? []
  const whollyMissing = options.find((o) => o.season === season)?.missing ?? false
  const numberWidth = `${String(Math.max(0, ...entries.map(entryNumber))).length + 1}ch`

  return (
    <div
      ref={popRef}
      id={id}
      popover="auto"
      role="dialog"
      aria-label="Episodes"
      data-player-controls
      onToggle={onToggle}
      style={{ position: 'fixed', ...placement }}
      className="bg-raised border-line text-primary shadow-overlay m-0 w-[min(340px,calc(100vw-16px))] animate-[menu-enter_var(--duration-base)_var(--easing-out)] flex-col overflow-hidden rounded-md border p-1 open:flex sm:w-[400px]"
    >
      {show.isPending ? (
        <div className="flex h-28 items-center justify-center">
          <Loader2 aria-hidden className="size-6 animate-spin text-secondary" strokeWidth={1.75} />
          <span className="sr-only">Loading episodes</span>
        </div>
      ) : show.isError || season == null ? (
        <p className="px-3 py-4 text-sm text-secondary">Couldn't load the episodes.</p>
      ) : (
        <>
          {options.length > 1 && (
            <>
              <div className="shrink-0 px-1">
                <SeasonChips
                  seasons={options}
                  selected={season}
                  onSelect={setPickedSeason}
                  surface="raised"
                  controls={`${id}-list`}
                />
              </div>
              <div role="separator" className="bg-line my-0.5 h-px shrink-0" />
            </>
          )}
          <div
            ref={listRef}
            id={`${id}-list`}
            className={`scrollbar-none relative max-h-[340px] min-h-0 snap-y snap-mandatory scroll-py-1 overflow-y-auto py-1 ${LIST_FADE}`}
          >
            {whollyMissing ? (
              <AllMissingRow episodes={entries.filter(isMissing)} />
            ) : (
              entries.map((entry) => (
                <EpisodeRow
                  key={entry.kind === 'item' ? entry.id : `missing-${entry.number}`}
                  entry={entry}
                  numberWidth={numberWidth}
                  playing={entry.kind === 'item' && entry.id === currentItemID}
                  onPick={(itemID) => {
                    popRef.current?.hidePopover()
                    if (itemID !== currentItemID) onPick(itemID)
                  }}
                />
              ))
            )}
          </div>
        </>
      )}
    </div>
  )
}

const ROW = 'flex h-[68px] w-full shrink-0 snap-start items-center gap-3 rounded-sm px-2 text-left'
const THUMB = 'aspect-video w-24 shrink-0 rounded-sm'

function EpisodeRow({
  entry,
  numberWidth,
  playing,
  onPick,
}: {
  entry: ShowEpisodeEntry
  numberWidth: string
  playing: boolean
  onPick: (itemID: number) => void
}) {
  if (entry.kind === 'missing') {
    return (
      <div aria-disabled="true" className={ROW}>
        <span className={`${THUMB} bg-raised border border-dashed border-tertiary`} />
        <RowText number={entry.number} width={numberWidth} title={entry.title} state={absentState(entry)} strong={!isUnaired(entry)} />
      </div>
    )
  }
  const progress = entry.progress
  const percent = playing ? 0 : progress?.completed ? 100 : progressPercent(progress?.position_s, entry.duration_s)
  const thumb = (
    <span className={`${THUMB} bg-inset relative overflow-hidden`}>
      {thumbnailGenerated(entry.thumb_url) && (
        <img src={entry.thumb_url} alt="" loading="lazy" className="h-full w-full object-cover" />
      )}
      {percent > 0 && (
        <span className="bg-progress-track absolute inset-x-0 bottom-0 h-[3px]">
          <span className="bg-accent-fill block h-full" style={{ width: `${percent}%` }} />
        </span>
      )}
    </span>
  )
  const number = entryNumber(entry)
  if (!entry.available) {
    return (
      <div aria-disabled="true" className={ROW}>
        {thumb}
        <RowText number={number} width={numberWidth} title={entry.title} state="Offline" strong />
      </div>
    )
  }
  const state = playing
    ? 'Playing'
    : [formatDuration(entry.duration_s), progress?.completed ? 'Watched' : ''].filter(Boolean).join(' · ')
  return (
    <button
      type="button"
      aria-current={playing ? 'true' : undefined}
      onClick={() => onPick(entry.id)}
      className={`${ROW} cursor-pointer ${playing ? 'bg-accent-subtle' : 'hover:bg-accent-subtle'}`}
    >
      {thumb}
      <RowText number={number} width={numberWidth} title={entry.title} state={state} strong={playing} />
    </button>
  )
}

function RowText({
  number,
  width,
  title,
  state,
  strong,
}: {
  number: number
  width: string
  title: string
  state: string
  strong: boolean
}) {
  return (
    <span className="flex min-w-0 flex-1 text-sm">
      <span className="tabular shrink-0 text-secondary" style={{ width }}>
        {number}
      </span>
      <span className="min-w-0">
        <span className="line-clamp-2 font-medium">{title}</span>
        <span className={`mt-0.5 block ${strong ? 'font-medium text-primary' : 'text-secondary'}`}>{state}</span>
      </span>
    </span>
  )
}

function AllMissingRow({ episodes }: { episodes: MissingEpisode[] }) {
  const unaired = episodes.length > 0 && episodes.every(isUnaired)
  return (
    <div aria-disabled="true" className={ROW}>
      <span className={`${THUMB} bg-raised border border-dashed border-tertiary`} />
      <span className={`text-sm ${unaired ? 'text-secondary' : 'font-medium text-primary'}`}>
        {unaired ? absentState(episodes[0]) : `All ${episodes.length} missing`}
      </span>
    </div>
  )
}

function visibleTrigger(container: Element | null | undefined): HTMLElement | undefined {
  return Array.from(container?.querySelectorAll<HTMLElement>(`[${EPISODES_TRIGGER}]`) ?? []).find(
    (element) => element.getClientRects().length > 0,
  )
}

function seasonOf(show: ShowDetail, itemID: number): number | null {
  const playing = show.seasons.find((s) => s.episodes.some((e) => e.kind === 'item' && e.id === itemID))
  return playing?.season ?? initialSeason(show)
}

function entryNumber(entry: ShowEpisodeEntry): number {
  return entry.kind === 'missing' ? entry.number : (entry.episode ?? 0)
}

function isMissing(entry: ShowEpisodeEntry): entry is MissingEpisode {
  return entry.kind === 'missing'
}

function isUnaired(entry: MissingEpisode): boolean {
  return entry.airdate != null && entry.airdate > localDate()
}

function absentState(entry: MissingEpisode): string {
  return isUnaired(entry) && entry.airdate ? `Airs ${formatAirdate(entry.airdate)}` : 'Missing'
}

function localDate(): string {
  const now = new Date()
  return `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}-${String(now.getDate()).padStart(2, '0')}`
}

/** `Fri 23 Oct`, with the year outside the current one. */
function formatAirdate(airdate: string): string {
  const date = new Date(`${airdate}T00:00:00Z`)
  const sameYear = date.getUTCFullYear() === new Date().getFullYear()
  return date.toLocaleDateString('en-GB', {
    weekday: 'short',
    day: 'numeric',
    month: 'short',
    year: sameYear ? undefined : 'numeric',
    timeZone: 'UTC',
  }).replace(',', '')
}
