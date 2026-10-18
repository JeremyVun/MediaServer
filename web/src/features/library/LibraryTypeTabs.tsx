import { Check } from 'lucide-react'
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

/** A checkbox, so it reads as combining with a tab rather than as a fourth
    one (ruling B18). Neutral on purpose: the screen's one amber accent stays
    on Upload and the active collection chip. */
export function UnwatchedCheckbox({ checked, onChange }: { checked: boolean; onChange: (next: boolean) => void }) {
  return (
    <label className="-my-3 inline-flex h-11 shrink-0 cursor-pointer items-center gap-2 whitespace-nowrap text-secondary hover:text-primary has-checked:text-primary">
      <span className="relative inline-flex size-4 shrink-0">
        <input
          type="checkbox"
          checked={checked}
          onChange={(event) => onChange(event.currentTarget.checked)}
          className="peer border-line-strong checked:bg-primary checked:border-primary size-4 cursor-pointer appearance-none rounded-[4px] border"
        />
        <Check
          aria-hidden
          strokeWidth={3}
          className="text-canvas pointer-events-none absolute inset-0 m-auto size-3 opacity-0 peer-checked:opacity-100"
        />
      </span>
      Unwatched
    </label>
  )
}
