import { forwardRef, type InputHTMLAttributes, type ReactNode } from 'react'

export interface InputProps extends InputHTMLAttributes<HTMLInputElement> {
  /** Leading glyph (e.g. a magnifier for search fields). */
  icon?: ReactNode
}

/**
 * Text input per DESIGN-SYSTEM: inset well, strong border, 40 px height,
 * focus ring via token (no border color swap).
 */
export const Input = forwardRef<HTMLInputElement, InputProps>(function Input(
  { icon, className, ...rest },
  ref,
) {
  return (
    <span className={['relative inline-flex w-full items-center', className ?? ''].join(' ')}>
      {icon && (
        <span
          aria-hidden
          className="text-tertiary pointer-events-none absolute left-3 inline-flex items-center"
        >
          {icon}
        </span>
      )}
      <input
        ref={ref}
        className={[
          'bg-inset text-primary placeholder:text-tertiary border-line-strong h-10 w-full',
          'rounded-sm border text-base',
          icon ? 'pr-3 pl-10' : 'px-3',
        ].join(' ')}
        {...rest}
      />
    </span>
  )
})
