import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from 'react'
import { Loader2 } from 'lucide-react'

export type ButtonVariant = 'primary' | 'secondary' | 'ghost' | 'danger'

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant
  /** 44 px height for primary touch-screen actions (default 36 px). */
  touch?: boolean
  /** Shows an in-button spinner and disables interaction. */
  pending?: boolean
  children?: ReactNode
}

const base =
  'inline-flex items-center justify-center gap-2 rounded-md text-base font-medium ' +
  'whitespace-nowrap select-none cursor-pointer ' +
  'transition-[background-color,color,transform] duration-[var(--duration-fast)] ease-out ' +
  'active:scale-[0.97] ' +
  'disabled:cursor-not-allowed disabled:opacity-50 disabled:active:scale-100'

const variants: Record<ButtonVariant, string> = {
  // Amber fill keeps the dark-theme amber in both themes (accent-fill-*).
  primary:
    'bg-accent-fill text-on-accent font-semibold hover:bg-accent-fill-hover active:bg-accent-fill-pressed',
  secondary: 'bg-surface border border-line-strong text-primary hover:bg-raised',
  ghost: 'bg-transparent text-primary hover:bg-accent-subtle',
  danger: 'bg-danger text-white hover:bg-danger-hover',
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = 'secondary', touch = false, pending = false, disabled, className, children, ...rest },
  ref,
) {
  return (
    <button
      ref={ref}
      type="button"
      disabled={disabled || pending}
      className={[
        base,
        variants[variant],
        touch ? 'h-11 px-5' : 'h-9 px-4',
        className ?? '',
      ].join(' ')}
      {...rest}
    >
      {pending && <Loader2 aria-hidden className="size-4 animate-spin" strokeWidth={1.75} />}
      {children}
    </button>
  )
})

export interface IconButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  /** Required: icon buttons have no visible text. */
  'aria-label': string
  variant?: ButtonVariant
  children: ReactNode
}

/** Square, ghost by default, ≥44 px touch target even when the glyph is smaller. */
export const IconButton = forwardRef<HTMLButtonElement, IconButtonProps>(function IconButton(
  { variant = 'ghost', className, children, ...rest },
  ref,
) {
  return (
    <button
      ref={ref}
      type="button"
      className={[base, variants[variant], 'size-11 shrink-0', className ?? ''].join(' ')}
      {...rest}
    >
      {children}
    </button>
  )
})
