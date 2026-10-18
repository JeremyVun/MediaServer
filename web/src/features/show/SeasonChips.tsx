import { useLayoutEffect, useRef, type KeyboardEvent } from 'react'
import { SHORT_LABEL_THRESHOLD, seasonAccessibleName, seasonLabel, type SeasonOption } from './seasons.ts'

export interface SeasonChipsProps {
  /** In display order: build with `seasonOptions(show.seasons)`. */
  seasons: SeasonOption[]
  selected: number
  onSelect: (season: number) => void
  /** The surface the row sits on. Dashed chips carry it as their own fill so
      their outline holds 3:1 over the show page's splash. */
  surface?: 'canvas' | 'raised'
  /** id of the element showing the selected season's episodes. */
  controls?: string
  className?: string
}

const FADE_MASK =
  '[mask-image:linear-gradient(to_right,transparent_0,black_16px,black_calc(100%-16px),transparent_100%)]'

/** The one season selector, shared by the show page and the player's
    Episodes popover: a single horizontally scrolling chip row that keeps the
    selected season in view. */
export function SeasonChips({
  seasons,
  selected,
  onSelect,
  surface = 'canvas',
  controls,
  className,
}: SeasonChipsProps) {
  const rowRef = useRef<HTMLDivElement>(null)
  const hasScrolled = useRef(false)
  const short = seasons.length > SHORT_LABEL_THRESHOLD

  useLayoutEffect(() => {
    const row = rowRef.current
    const chip = row?.querySelector<HTMLElement>('[aria-selected="true"]')
    if (!row || !chip) return
    const left = chip.offsetLeft - (row.clientWidth - chip.offsetWidth) / 2
    const reduce = window.matchMedia('(prefers-reduced-motion: reduce)').matches
    row.scrollTo({ left, behavior: hasScrolled.current && !reduce ? 'smooth' : 'auto' })
    hasScrolled.current = true
  }, [selected, seasons.length])

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const step = event.key === 'ArrowRight' ? 1 : event.key === 'ArrowLeft' ? -1 : 0
    if (!step) return
    const index = seasons.findIndex((s) => s.season === selected)
    const next = seasons[index + step]
    if (!next) return
    event.preventDefault()
    onSelect(next.season)
    rowRef.current
      ?.querySelector<HTMLElement>(`[data-season="${next.season}"]`)
      ?.focus({ preventScroll: true })
  }

  const missingFill = surface === 'raised' ? 'bg-raised' : 'bg-canvas'

  return (
    <div
      ref={rowRef}
      role="tablist"
      aria-label="Seasons"
      onKeyDown={onKeyDown}
      className={[
        'scrollbar-none relative flex items-center gap-1 overflow-x-auto px-3',
        FADE_MASK,
        className ?? '',
      ].join(' ')}
    >
      {seasons.map((option) => {
        const on = option.season === selected
        const look = on
          ? 'bg-primary text-canvas border-transparent font-medium'
          : option.missing
            ? `${missingFill} border-dashed border-tertiary text-secondary`
            : 'border-line text-primary hover:bg-accent-subtle'
        return (
          <button
            key={option.season}
            type="button"
            role="tab"
            data-season={option.season}
            aria-selected={on}
            aria-controls={controls}
            aria-label={seasonAccessibleName(option)}
            tabIndex={on ? 0 : -1}
            onClick={() => onSelect(option.season)}
            className="inline-flex h-11 shrink-0 cursor-pointer items-center"
          >
            <span
              className={[
                'inline-flex h-8 min-w-11 items-center justify-center rounded-full border px-3 text-sm whitespace-nowrap',
                look,
              ].join(' ')}
            >
              {seasonLabel(option.season, short)}
            </span>
          </button>
        )
      })}
    </div>
  )
}
