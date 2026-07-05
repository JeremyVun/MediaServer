import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from 'react'

export interface ToastOptions {
  message: string
  /** Optional action, rendered in amber (e.g. "Undo"). */
  action?: {
    label: string
    onClick: () => void
  }
}

interface QueuedToast extends ToastOptions {
  id: number
}

interface ToastContextValue {
  toast: (opts: ToastOptions) => void
}

const ToastContext = createContext<ToastContextValue | null>(null)

const TOAST_MS = 6000

/**
 * Toast host per DESIGN-SYSTEM: bottom-center, raised surface, auto-dismiss
 * after 6 s with the timer paused on hover, max one visible — the rest
 * queue. Announced politely to screen readers.
 */
export function ToastProvider({ children }: { children: ReactNode }) {
  const [queue, setQueue] = useState<QueuedToast[]>([])
  const nextId = useRef(1)

  const toast = useCallback((opts: ToastOptions) => {
    setQueue((q) => [...q, { ...opts, id: nextId.current++ }])
  }, [])

  const dismiss = useCallback((id: number) => {
    setQueue((q) => q.filter((t) => t.id !== id))
  }, [])

  const current = queue[0]

  return (
    <ToastContext.Provider value={{ toast }}>
      {children}
      <div
        aria-live="polite"
        className="pointer-events-none fixed inset-x-0 bottom-6 z-[var(--z-toast)] flex justify-center px-4"
      >
        {current && <ToastCard key={current.id} toast={current} onDismiss={dismiss} />}
      </div>
    </ToastContext.Provider>
  )
}

function ToastCard({ toast, onDismiss }: { toast: QueuedToast; onDismiss: (id: number) => void }) {
  const remaining = useRef(TOAST_MS)
  const startedAt = useRef(0)
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined)

  const clear = () => clearTimeout(timer.current)

  const arm = useCallback(() => {
    startedAt.current = Date.now()
    timer.current = setTimeout(() => onDismiss(toast.id), remaining.current)
  }, [toast.id, onDismiss])

  useEffect(() => {
    arm()
    return clear
  }, [arm])

  const pause = () => {
    clear()
    remaining.current -= Date.now() - startedAt.current
  }

  return (
    <div
      onMouseEnter={pause}
      onMouseLeave={arm}
      className={[
        'bg-raised text-primary shadow-overlay pointer-events-auto flex items-center gap-4',
        'rounded-md py-3 pr-3 pl-4 text-base',
        'animate-[toast-enter_var(--duration-slow)_var(--easing-spring)]',
      ].join(' ')}
    >
      <span>{toast.message}</span>
      {toast.action && (
        <button
          type="button"
          className="text-accent hover:text-accent-hover cursor-pointer rounded-sm font-semibold"
          onClick={() => {
            toast.action?.onClick()
            onDismiss(toast.id)
          }}
        >
          {toast.action.label}
        </button>
      )}
    </div>
  )
}

// eslint-disable-next-line react-refresh/only-export-components
export function useToast(): ToastContextValue {
  const ctx = useContext(ToastContext)
  if (!ctx) throw new Error('useToast must be used inside ToastProvider')
  return ctx
}
