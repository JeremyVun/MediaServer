import { useEffect, useRef, type ReactNode } from 'react'
import { Check } from 'lucide-react'
import { Card } from '../../ui/index.ts'

export interface CardSelection {
  active: boolean
  selected: boolean
  /** Long-press on touch enters selection mode with this card selected. */
  onEnter: () => void
  onToggle: (shiftRange: boolean) => void
}

/** A library card that can join selection mode. Without `selection` it is a
    plain interactive card (the Continue watching row). */
export function SelectableCard({
  label,
  selection,
  justArrived = false,
  children,
}: {
  label: string
  selection?: CardSelection
  justArrived?: boolean
  children: ReactNode
}) {
  // The press is cancelled by lift or movement; when it fires, the release
  // click is swallowed so it doesn't immediately untoggle what it selected.
  const longPressTimer = useRef<number | null>(null)
  const pressOrigin = useRef<{ x: number; y: number } | null>(null)
  const suppressClick = useRef(false)
  useEffect(
    () => () => {
      if (longPressTimer.current != null) window.clearTimeout(longPressTimer.current)
    },
    [],
  )
  const cancelLongPress = () => {
    if (longPressTimer.current != null) {
      window.clearTimeout(longPressTimer.current)
      longPressTimer.current = null
    }
    pressOrigin.current = null
  }

  if (!selection) return <Card interactive>{children}</Card>

  return (
    <Card
      interactive
      className={[
        'group relative select-none [-webkit-touch-callout:none]',
        selection.selected ? 'ring-2 ring-accent' : '',
        justArrived ? 'animate-[card-enter_var(--duration-slow)_var(--easing-out)]' : '',
      ].join(' ')}
      onPointerDown={(event) => {
        if (event.pointerType === 'mouse' || selection.active) return
        pressOrigin.current = { x: event.clientX, y: event.clientY }
        longPressTimer.current = window.setTimeout(() => {
          longPressTimer.current = null
          suppressClick.current = true
          selection.onEnter()
        }, 500)
      }}
      onPointerMove={(event) => {
        const origin = pressOrigin.current
        if (!origin || longPressTimer.current == null) return
        if (Math.hypot(event.clientX - origin.x, event.clientY - origin.y) > 10) cancelLongPress()
      }}
      onPointerUp={cancelLongPress}
      onPointerCancel={cancelLongPress}
      onPointerLeave={cancelLongPress}
      onClickCapture={
        selection.active
          ? (event) => {
              event.preventDefault()
              event.stopPropagation()
              if (suppressClick.current) {
                suppressClick.current = false
                return
              }
              selection.onToggle(event.shiftKey)
            }
          : undefined
      }
    >
      {children}
      {/* The card's capture handler owns the toggle, so a click anywhere
          counts; a focusable role=checkbox keeps keyboard and screen-reader
          selection working. */}
      {selection.active && (
        <button
          type="button"
          role="checkbox"
          aria-checked={selection.selected}
          aria-label={`Select ${label}`}
          className={[
            'absolute top-2 left-2 z-20 flex size-6 items-center justify-center rounded-full border',
            selection.selected
              ? 'bg-accent-fill text-on-accent border-transparent'
              : 'bg-overlay border-white/70 backdrop-blur-sm',
          ].join(' ')}
        >
          {selection.selected && <Check aria-hidden className="size-4" strokeWidth={2.5} />}
        </button>
      )}
    </Card>
  )
}
