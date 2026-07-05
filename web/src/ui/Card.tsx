import { forwardRef, type HTMLAttributes } from 'react'

export interface CardProps extends HTMLAttributes<HTMLDivElement> {
  /**
   * Interactive cards raise on hover/focus: bg-raised + shadow + scale 1.02
   * (the poster-card treatment).
   */
  interactive?: boolean
}

/** Surface-level container: bg-surface, radius lg, hairline border. */
export const Card = forwardRef<HTMLDivElement, CardProps>(function Card(
  { interactive = false, className, children, ...rest },
  ref,
) {
  return (
    <div
      ref={ref}
      className={[
        'bg-surface rounded-lg border border-line overflow-hidden',
        interactive
          ? 'transition-[background-color,box-shadow,transform] duration-[var(--duration-fast)] ease-out ' +
            'hover:bg-raised hover:shadow-raised hover:scale-[1.02] ' +
            'focus-within:bg-raised focus-within:shadow-raised'
          : '',
        className ?? '',
      ].join(' ')}
      {...rest}
    >
      {children}
    </div>
  )
})
