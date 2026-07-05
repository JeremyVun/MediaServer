import { useEffect, useRef, type ReactNode } from 'react'
import { X } from 'lucide-react'
import { IconButton } from './Button.tsx'

export interface DialogProps {
  open: boolean
  onClose: () => void
  title: string
  /** 'form' → 440 px max width, 'content' → 560 px. */
  width?: 'form' | 'content'
  children: ReactNode
  /** Action row, right-aligned. Destructive confirms put the danger action
      on the right; never make it the default focus. */
  footer?: ReactNode
}

/**
 * Modal dialog on the native <dialog> element: focus trap, Esc-to-close and
 * top-layer rendering come from the platform. Styled per DESIGN-SYSTEM
 * (raised surface, overlay scrim + blur, scale 0.97→1 enter).
 */
export function Dialog({ open, onClose, title, width = 'form', children, footer }: DialogProps) {
  const ref = useRef<HTMLDialogElement>(null)

  useEffect(() => {
    const dialog = ref.current
    if (!dialog) return
    if (open && !dialog.open) {
      dialog.showModal()
    } else if (!open && dialog.open) {
      dialog.close()
    }
  }, [open])

  return (
    <dialog
      ref={ref}
      aria-label={title}
      onCancel={(e) => {
        e.preventDefault() // keep open-state ownership in React
        onClose()
      }}
      onClick={(e) => {
        if (e.target === ref.current) onClose() // click on the scrim
      }}
      className={[
        'bg-raised text-primary shadow-overlay m-auto w-[calc(100vw-32px)] rounded-lg p-0',
        width === 'form' ? 'max-w-[440px]' : 'max-w-[560px]',
        'backdrop:bg-overlay backdrop:backdrop-blur-[20px]',
        'open:animate-[dialog-enter_var(--duration-slow)_var(--easing-out)]',
      ].join(' ')}
    >
      <header className="flex items-center justify-between py-2 pr-2 pl-5">
        <h2 className="text-lg font-semibold tracking-tight">{title}</h2>
        <IconButton aria-label="Close" onClick={onClose}>
          <X aria-hidden className="size-5" strokeWidth={1.75} />
        </IconButton>
      </header>
      <div className="px-5 pb-5">{children}</div>
      {footer && <footer className="border-line flex justify-end gap-2 border-t p-4">{footer}</footer>}
    </dialog>
  )
}
