import type { HTMLAttributes } from 'react'

/**
 * Loading placeholder with the 1.6 s sheen sweep. Size it with className
 * (e.g. aspect-[2/3] for poster shapes). Content areas always skeleton —
 * spinners are reserved for in-button pending states.
 */
export function Skeleton({ className, ...rest }: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      aria-hidden
      className={['bg-skeleton rounded-lg', className ?? ''].join(' ')}
      style={{
        backgroundImage:
          'linear-gradient(90deg, var(--color-skeleton) 40%, var(--color-skeleton-sheen) 50%, var(--color-skeleton) 60%)',
        backgroundSize: '200% 100%',
        animation: 'skeleton-sheen 1.6s linear infinite',
      }}
      {...rest}
    />
  )
}
