import type { ReactNode } from 'react'

/** Minimal card style: title (and an optional second line) over a scrim at
    the image's foot. */
export function CardOverlay({ title, subtitle }: { title: string; subtitle?: string }) {
  return (
    <div className="pointer-events-none absolute inset-x-0 bottom-0 bg-gradient-to-t from-black/85 via-black/30 to-transparent px-2.5 pt-10 pb-2.5">
      <h2 className="truncate text-sm font-medium text-white">{title}</h2>
      {subtitle && <p className="tabular truncate text-xs text-white/75">{subtitle}</p>}
    </div>
  )
}

export function ProgressEdge({ percent }: { percent: number }) {
  if (percent <= 0) return null
  return (
    <span className="bg-progress-track absolute inset-x-0 bottom-0 h-[3px]">
      <span className="bg-accent-fill block h-full rounded-full" style={{ width: `${percent}%` }} />
    </span>
  )
}

/** Compact card style: a tight title with a smaller meta line below the image. */
export function CardMeta({ title, meta }: { title: string; meta: string }) {
  return (
    <div className="p-2">
      <h2 className="truncate text-sm font-medium leading-snug">{title}</h2>
      <p className="tabular mt-0.5 truncate text-xs text-secondary">{meta}</p>
    </div>
  )
}

export function ImagePlaceholder({ children }: { children: ReactNode }) {
  return <div className="text-tertiary flex h-full w-full items-center justify-center">{children}</div>
}
