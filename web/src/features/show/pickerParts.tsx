import { useState, type ReactNode } from 'react'
import { Ban, Tv } from 'lucide-react'

/** One choice in a picker dialog: a poster, a name and a meta line, with a
    radio mark. The selected row gets a surface and a strong border. */
export function OptionRow({
  thumb,
  title,
  meta,
  selected,
  onSelect,
}: {
  thumb: ReactNode
  title: string
  meta: ReactNode
  selected: boolean
  onSelect: () => void
}) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={selected}
      onClick={onSelect}
      className={[
        'flex w-full cursor-pointer items-center gap-3 rounded-md border p-2 text-left',
        selected ? 'border-line-strong bg-surface' : 'hover:border-line border-transparent',
      ].join(' ')}
    >
      {thumb}
      <span className="min-w-0 flex-1">
        <span className="line-clamp-2 block text-base font-medium">{title}</span>
        {meta && <span className="block text-sm text-secondary">{meta}</span>}
      </span>
      <span
        aria-hidden
        className={[
          'inline-flex size-5 shrink-0 items-center justify-center rounded-full border-2',
          selected ? 'border-primary' : 'border-line-strong',
        ].join(' ')}
      >
        {selected && <span className="bg-primary size-2.5 rounded-full" />}
      </span>
    </button>
  )
}

const THUMB = 'bg-inset border-line aspect-[2/3] w-10 shrink-0 overflow-hidden rounded-sm border'

export function PosterThumb({ src }: { src: string | null }) {
  const [broken, setBroken] = useState(false)
  if (!src || broken) {
    return (
      <span className={`${THUMB} text-tertiary flex items-center justify-center`}>
        <Tv aria-hidden className="size-4" strokeWidth={1.75} />
      </span>
    )
  }
  return (
    <span className={`${THUMB} block`}>
      <img src={src} alt="" loading="lazy" onError={() => setBroken(true)} className="h-full w-full object-cover" />
    </span>
  )
}

export function NoneThumb() {
  return (
    <span className="border-tertiary text-secondary flex aspect-[2/3] w-10 shrink-0 items-center justify-center rounded-sm border border-dashed">
      <Ban aria-hidden className="size-4" strokeWidth={1.75} />
    </span>
  )
}

/** The candidate list's scroll area: five rows and half of a sixth, so a
    longer list shows that it scrolls, and short enough on a phone that the
    Change dialog's season row and footer still fit on screen. */
export const LIST = 'scrollbar-subtle -mx-2 mt-3 max-h-[min(28rem,calc(100dvh-30rem))] space-y-1 overflow-y-auto px-2'
