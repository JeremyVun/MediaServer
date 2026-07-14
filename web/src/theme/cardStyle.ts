import { useSyncExternalStore } from 'react'

// Library grid card density/treatment. A local display preference (like the
// theme): stored in localStorage, no server round-trip. 'minimal' is the
// default and is not persisted, so clearing storage returns to it.
export type CardStyle = 'compact' | 'minimal'

const DEFAULT_CARD_STYLE: CardStyle = 'minimal'

export const CARD_STYLES: { value: CardStyle; label: string }[] = [
  { value: 'compact', label: 'Compact' },
  { value: 'minimal', label: 'Minimal' },
]

const STORAGE_KEY = 'card-style'
// Same-tab consumers don't see the native 'storage' event (it only fires in
// other tabs), so writes dispatch this to notify every useCardStyle() here.
const CHANGE_EVENT = 'card-style-change'
const VALUES = new Set<CardStyle>(CARD_STYLES.map((s) => s.value))

function read(): CardStyle {
  const stored = localStorage.getItem(STORAGE_KEY)
  return stored && VALUES.has(stored as CardStyle) ? (stored as CardStyle) : DEFAULT_CARD_STYLE
}

function subscribe(onChange: () => void): () => void {
  window.addEventListener(CHANGE_EVENT, onChange)
  window.addEventListener('storage', onChange) // cross-tab
  return () => {
    window.removeEventListener(CHANGE_EVENT, onChange)
    window.removeEventListener('storage', onChange)
  }
}

export function setCardStyle(style: CardStyle): void {
  if (style === DEFAULT_CARD_STYLE) localStorage.removeItem(STORAGE_KEY)
  else localStorage.setItem(STORAGE_KEY, style)
  window.dispatchEvent(new Event(CHANGE_EVENT))
}

/** Subscribe to the current card style; re-renders on change (any tab). */
export function useCardStyle(): CardStyle {
  return useSyncExternalStore(subscribe, read, () => DEFAULT_CARD_STYLE)
}
