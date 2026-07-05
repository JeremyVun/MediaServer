import {
  createContext,
  useContext,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type ButtonHTMLAttributes,
  type CSSProperties,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
  type SyntheticEvent,
} from 'react'
import { Check } from 'lucide-react'
import { placeMenu } from './menuPlacement.ts'

/* ------------------------------------------------------------------------
   Menu — dropdown on the native Popover API: top-layer rendering (immune
   to overflow-hidden ancestors and stacking contexts) and light dismiss
   (outside click, Escape) come from the platform.

   Nested views: a MenuItem may switch the menu body via setView (see
   useMenu); the view resets when the menu closes. Consumers pass children
   as a render function to branch on the current view.
   ------------------------------------------------------------------------ */

export interface MenuContextValue {
  close: () => void
  /** Current nested view, or null for the root item list. */
  view: string | null
  setView: (view: string | null) => void
}

const MenuContext = createContext<MenuContextValue | null>(null)

/** Menu body context — close the menu or switch nested views. */
// eslint-disable-next-line react-refresh/only-export-components
export function useMenu(): MenuContextValue {
  const ctx = useContext(MenuContext)
  if (!ctx) throw new Error('useMenu must be used inside <Menu>')
  return ctx
}

export interface MenuProps {
  /** Trigger button content (icon and/or label). */
  trigger: ReactNode
  /** Styles the trigger button; it has no default chrome beyond focus ring. */
  triggerClassName?: string
  'aria-label'?: string
  align?: 'start' | 'end'
  /** Width/size overrides for the menu surface (default min-w-48). */
  menuClassName?: string
  onOpenChange?: (open: boolean) => void
  children: ReactNode | ((ctx: MenuContextValue) => ReactNode)
}

export function Menu({
  trigger,
  triggerClassName,
  'aria-label': ariaLabel,
  align = 'end',
  menuClassName,
  onOpenChange,
  children,
}: MenuProps) {
  const id = useId()
  const triggerRef = useRef<HTMLButtonElement>(null)
  const popRef = useRef<HTMLDivElement>(null)
  const [open, setOpen] = useState(false)
  const [view, setView] = useState<string | null>(null)
  const [style, setStyle] = useState<CSSProperties>()

  const close = () => popRef.current?.hidePopover()
  const ctx: MenuContextValue = { close, view, setView }

  const onToggle = (event: SyntheticEvent<HTMLDivElement>) => {
    const isOpen = (event.nativeEvent as ToggleEvent).newState === 'open'
    setOpen(isOpen)
    onOpenChange?.(isOpen)
    if (!isOpen) {
      setView(null)
      setStyle(undefined)
      // The platform restores focus to the invoker when focus was inside
      // the popover; cover programmatic closes where it wasn't.
      if (document.activeElement === document.body) triggerRef.current?.focus()
    }
  }

  // Position before paint, then track trigger movement (scroll/resize) and
  // menu size changes (nested views filter/grow) while open.
  useLayoutEffect(() => {
    if (!open) return
    const reposition = () => {
      const button = triggerRef.current
      const pop = popRef.current
      if (!button || !pop) return
      const rect = button.getBoundingClientRect()
      const placed = placeMenu({
        trigger: rect,
        menu: { width: pop.offsetWidth, height: pop.scrollHeight },
        viewport: { width: window.innerWidth, height: window.innerHeight },
        align,
      })
      setStyle({
        position: 'fixed',
        top: placed.top,
        left: placed.left,
        maxHeight: placed.maxHeight,
      })
    }
    reposition()
    const observer = new ResizeObserver(reposition)
    if (popRef.current) observer.observe(popRef.current)
    window.addEventListener('resize', reposition)
    window.addEventListener('scroll', reposition, true)
    return () => {
      observer.disconnect()
      window.removeEventListener('resize', reposition)
      window.removeEventListener('scroll', reposition, true)
    }
  }, [open, align, view])

  // Focus the first item (or the nested view's input) when opening or
  // switching views.
  useLayoutEffect(() => {
    if (!open) return
    const pop = popRef.current
    if (!pop) return
    const target = pop.querySelector<HTMLElement>(
      'input:not([disabled]), [role^="menuitem"]:not([disabled])',
    )
    target?.focus()
  }, [open, view])

  const onKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    const pop = popRef.current
    if (!pop) return
    const inInput = event.target instanceof HTMLInputElement
    if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return
    if (inInput && (event.key === 'Home' || event.key === 'End')) return // caret movement
    const items = Array.from(
      pop.querySelectorAll<HTMLElement>('input:not([disabled]), [role^="menuitem"]:not([disabled])'),
    )
    if (items.length === 0) return
    event.preventDefault()
    const current = items.indexOf(document.activeElement as HTMLElement)
    let next: number
    if (event.key === 'Home') next = 0
    else if (event.key === 'End') next = items.length - 1
    else if (event.key === 'ArrowDown') next = current < items.length - 1 ? current + 1 : 0
    else next = current > 0 ? current - 1 : items.length - 1
    items[next]?.focus()
  }

  return (
    <>
      <button
        ref={triggerRef}
        type="button"
        popoverTarget={id}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={ariaLabel}
        className={triggerClassName ?? ''}
      >
        {trigger}
      </button>
      <div
        ref={popRef}
        id={id}
        popover="auto"
        role="menu"
        aria-label={ariaLabel}
        onToggle={onToggle}
        onKeyDown={onKeyDown}
        style={style}
        className={[
          'bg-raised border-line text-primary m-0 overflow-y-auto rounded-md border p-1 shadow-overlay',
          'animate-[menu-enter_var(--duration-base)_var(--easing-out)]',
          menuClassName ?? 'min-w-48',
        ].join(' ')}
      >
        <MenuContext.Provider value={ctx}>
          {/* ctx.close reads the popover ref inside an event handler only,
              never during render — the refs rule can't see that. */}
          {/* eslint-disable-next-line react-hooks/refs */}
          {typeof children === 'function' ? children(ctx) : children}
        </MenuContext.Provider>
      </div>
    </>
  )
}

/* ------------------------------------------------------------------------
   Items
   ------------------------------------------------------------------------ */

export interface MenuItemProps
  extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'onSelect' | 'role'> {
  icon?: ReactNode
  /** Destructive action styling. */
  danger?: boolean
  /** Renders a trailing checkmark and menuitemcheckbox semantics. */
  checked?: boolean
  /** Trailing adornment, e.g. a ChevronRight for a nested view. */
  trailing?: ReactNode
  onSelect?: () => void
  /** Keep the menu open after selecting (toggles inside nested views). */
  closeOnSelect?: boolean
  children: ReactNode
}

export function MenuItem({
  icon,
  danger = false,
  checked,
  trailing,
  onSelect,
  closeOnSelect = true,
  disabled,
  className,
  children,
  ...rest
}: MenuItemProps) {
  const { close } = useMenu()
  return (
    <button
      type="button"
      role={checked === undefined ? 'menuitem' : 'menuitemcheckbox'}
      aria-checked={checked}
      tabIndex={-1}
      disabled={disabled}
      onClick={() => {
        onSelect?.()
        if (closeOnSelect) close()
      }}
      className={[
        'flex w-full cursor-pointer items-center gap-2 rounded-sm px-3 py-2 text-left text-sm',
        'transition-colors duration-[var(--duration-fast)]',
        danger
          ? 'text-danger hover:bg-danger/10 focus-visible:bg-danger/10'
          : 'text-primary hover:bg-accent-subtle focus-visible:bg-accent-subtle',
        'disabled:cursor-not-allowed disabled:text-disabled disabled:hover:bg-transparent',
        className ?? '',
      ].join(' ')}
      {...rest}
    >
      {icon && (
        <span aria-hidden className="flex size-4 shrink-0 items-center justify-center">
          {icon}
        </span>
      )}
      <span className="min-w-0 flex-1 truncate">{children}</span>
      {checked && <Check aria-hidden className="size-4 shrink-0" strokeWidth={1.75} />}
      {trailing && (
        <span aria-hidden className="text-tertiary flex shrink-0 items-center">
          {trailing}
        </span>
      )}
    </button>
  )
}

export function MenuSeparator() {
  return <div role="separator" className="bg-line my-1 h-px" />
}
