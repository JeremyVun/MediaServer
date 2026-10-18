import type { ReactNode } from 'react'
import { HardDrive } from 'lucide-react'

/** Top-right status slot on an image card. One badge per card: Offline wins
    over Check. */
export function CardBadge({ children }: { children: ReactNode }) {
  return (
    <span className="bg-raised text-secondary border-line absolute top-2 right-2 z-10 inline-flex h-6 items-center gap-1 rounded-sm border px-1.5 text-xs font-medium">
      {children}
    </span>
  )
}

export function OfflineBadge() {
  return (
    <CardBadge>
      <HardDrive aria-hidden className="size-3.5 shrink-0" strokeWidth={1.75} />
      Offline
    </CardBadge>
  )
}

export function CheckBadge() {
  return <CardBadge>Check</CardBadge>
}

/** Inline form for meta lines and rows: `Offline` leads the line. */
export function OfflineLabel({ className }: { className?: string }) {
  return (
    <span className={['inline-flex items-center gap-1 font-medium', className ?? 'text-primary'].join(' ')}>
      <HardDrive aria-hidden className="size-4 shrink-0" strokeWidth={1.75} />
      Offline
    </span>
  )
}
