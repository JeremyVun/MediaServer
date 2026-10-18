import type { LibraryType } from '../../lib/searchParams.ts'

const TABS: { value: LibraryType; label: string }[] = [
  { value: '', label: 'All' },
  { value: 'show', label: 'Shows' },
  { value: 'movie', label: 'Movies' },
]

/** All · Shows · Movies, in place of the old item-count line. Negative
    margins give each tab a 44 px target without growing the row. */
export function LibraryTypeTabs({
  value,
  onChange,
}: {
  value: LibraryType
  onChange: (next: LibraryType) => void
}) {
  return (
    <div role="tablist" aria-label="Show" className="flex items-center gap-6">
      {TABS.map((tab) => {
        const on = tab.value === value
        return (
          <button
            key={tab.label}
            type="button"
            role="tab"
            aria-selected={on}
            onClick={() => onChange(tab.value)}
            className={[
              'relative -mx-3 -my-3 inline-flex h-11 min-w-11 cursor-pointer items-center justify-center px-3',
              on ? 'text-primary font-medium' : 'text-secondary hover:text-primary',
            ].join(' ')}
          >
            {tab.label}
            {on && <span aria-hidden className="bg-primary absolute inset-x-3 bottom-1.5 h-0.5 rounded-full" />}
          </button>
        )
      })}
    </div>
  )
}

/** Neutral on purpose: the screen's one amber accent stays on Upload and
    the active collection chip. */
export function UnwatchedToggle({ pressed, onChange }: { pressed: boolean; onChange: (next: boolean) => void }) {
  return (
    <button
      type="button"
      aria-pressed={pressed}
      onClick={() => onChange(!pressed)}
      className="group -my-3 inline-flex h-11 shrink-0 cursor-pointer items-center"
    >
      <span
        className={[
          'inline-flex h-7 items-center rounded-full border px-3 whitespace-nowrap',
          pressed
            ? 'bg-primary border-primary text-canvas font-medium'
            : 'border-line-strong text-secondary group-hover:text-primary',
        ].join(' ')}
      >
        Unwatched
      </span>
    </button>
  )
}
