import { useCallback, useEffect, useLayoutEffect, type RefObject } from 'react'
import { useTheme } from '../../theme/ThemeProvider.tsx'

/** Drives the splash from the scroll: --dim eases in over 65 % of the art's
    height, --drift moves the art at 0.6x the scroll, --top fades the back
    link's scrim over 160 px. */
export function useSplashScroll(pageRef: RefObject<HTMLElement | null>, bandRef: RefObject<HTMLElement | null>) {
  const { resolved } = useTheme()

  const update = useCallback(() => {
    const page = pageRef.current
    const band = bandRef.current
    if (!page || !band) return
    const height = band.offsetHeight
    const k = Math.min(1, window.scrollY / Math.max(1, height * 0.65))
    page.style.setProperty('--dim', (k * k * (3 - 2 * k)).toFixed(3))
    page.style.setProperty('--drift', `${(-Math.min(window.scrollY * 0.6, height)).toFixed(1)}px`)
    page.style.setProperty('--top', (1 - Math.min(1, window.scrollY / 160)).toFixed(3))
  }, [pageRef, bandRef])

  useEffect(() => {
    let frame = 0
    const schedule = () => {
      cancelAnimationFrame(frame)
      frame = requestAnimationFrame(update)
    }
    window.addEventListener('scroll', schedule, { passive: true })
    window.addEventListener('resize', schedule)
    update()
    return () => {
      cancelAnimationFrame(frame)
      window.removeEventListener('scroll', schedule)
      window.removeEventListener('resize', schedule)
    }
  }, [update])

  // Custom themes declare their own color-scheme; light ones get the
  // stronger wash that keeps dashed tiles at 3:1.
  useLayoutEffect(() => {
    const page = pageRef.current
    if (!page) return
    const light = getComputedStyle(document.documentElement).colorScheme.includes('light')
    page.dataset.scheme = light ? 'light' : 'dark'
  }, [pageRef, resolved])

  return update
}
