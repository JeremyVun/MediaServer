import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from 'react'
import { THEME_VALUES } from './registry.ts'

// 'system' | built-in | any custom theme name from the registry. The
// (string & {}) member keeps literal autocomplete while allowing any value.
export type ThemePreference = 'system' | 'dark' | 'light' | (string & {})
export type ResolvedTheme = string

interface ThemeContextValue {
  /** The stored preference (may be 'system'). */
  preference: ThemePreference
  /** What is actually applied right now. */
  resolved: ResolvedTheme
  setPreference: (pref: ThemePreference) => void
}

const ThemeContext = createContext<ThemeContextValue | null>(null)

const STORAGE_KEY = 'theme'

function readPreference(): ThemePreference {
  const stored = localStorage.getItem(STORAGE_KEY)
  // Validate against the live registry so a deleted theme resets to system.
  return stored && THEME_VALUES.has(stored) ? stored : 'system'
}

function systemTheme(): ResolvedTheme {
  return matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark'
}

function resolve(pref: ThemePreference): ResolvedTheme {
  return pref === 'system' ? systemTheme() : pref
}

/**
 * Owns the `data-theme` attribute on <html>. The inline script in
 * index.html applies it before first paint; this provider keeps it in sync
 * with the stored preference and live OS scheme changes.
 */
export function ThemeProvider({ children }: { children: ReactNode }) {
  const [preference, setPreferenceState] = useState<ThemePreference>(readPreference)
  const [resolved, setResolved] = useState<ResolvedTheme>(() => resolve(readPreference()))

  useEffect(() => {
    document.documentElement.dataset.theme = resolved
  }, [resolved])

  // Follow live OS scheme changes while preference is 'system'.
  useEffect(() => {
    if (preference !== 'system') return
    const mq = matchMedia('(prefers-color-scheme: light)')
    const onChange = () => setResolved(systemTheme())
    mq.addEventListener('change', onChange)
    return () => mq.removeEventListener('change', onChange)
  }, [preference])

  const setPreference = useCallback((pref: ThemePreference) => {
    setPreferenceState(pref)
    setResolved(resolve(pref))
    if (pref === 'system') {
      localStorage.removeItem(STORAGE_KEY)
    } else {
      localStorage.setItem(STORAGE_KEY, pref)
    }
  }, [])

  const value = useMemo(
    () => ({ preference, resolved, setPreference }),
    [preference, resolved, setPreference],
  )

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>
}

// eslint-disable-next-line react-refresh/only-export-components
export function useTheme(): ThemeContextValue {
  const ctx = useContext(ThemeContext)
  if (!ctx) throw new Error('useTheme must be used inside ThemeProvider')
  return ctx
}
