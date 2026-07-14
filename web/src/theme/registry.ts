// Custom-theme auto-discovery.
//
// Drop a `NAME.theme.css` file in ./themes and it appears everywhere a theme
// can be chosen — the Appearance picker, the pre-paint allowlist, and the
// AA contrast test — with no other code changes. See ./themes/_template.css
// for the authoring contract.
//
// How it works: every theme file is one `:root[data-theme='NAME'] { … }`
// block overriding the raw --color-* tokens from tokens.css. Because every
// Tailwind utility resolves through those custom properties, the block
// re-skins the whole app with zero component changes. The (0,2,0) selector
// out-specifies the built-in `:root` dark block, so overrides win regardless
// of import order.

import tokensCss from './tokens.css?raw'

export type ThemeBase = 'dark' | 'light'

/** Representative colours for the picker's preview tile. */
export interface ThemeSwatch {
  canvas: string
  surface: string
  accent: string
  text: string
}

export interface ThemeOption {
  /** data-theme value and localStorage key; equals the file stem. */
  value: string
  /** Human label shown in the picker. */
  label: string
  /** Palette family the theme belongs to (drives color-scheme / icon). */
  base: ThemeBase
  /** A few resolved colours, so the picker can render a preview tile. */
  swatch: ThemeSwatch
}

// Parse `--name: value;` declarations from a CSS block body.
function declarations(body: string): Record<string, string> {
  const out: Record<string, string> = {}
  const re = /(--[\w-]+)\s*:\s*([^;]+);/g
  let m: RegExpExecArray | null
  while ((m = re.exec(body))) out[m[1]] = m[2].trim()
  return out
}

// Built-in dark is the baseline every custom theme cascades over (its `:root`
// block matches every data-theme); light is the other full built-in palette.
const DARK = declarations(/:root,\s*\[data-theme='dark'\]\s*\{([\s\S]*?)\}/.exec(tokensCss)?.[1] ?? '')
const LIGHT = declarations(/\[data-theme='light'\]\s*\{([\s\S]*?)\}/.exec(tokensCss)?.[1] ?? '')

// A theme only overrides some tokens; anything unset falls back to DARK (the
// cascade base), so the swatch resolves the same way the browser would.
function swatch(decls: Record<string, string>): ThemeSwatch {
  const g = (k: string) => decls[k] ?? DARK[k] ?? '#000000'
  return {
    canvas: g('--color-bg-canvas'),
    surface: g('--color-bg-surface'),
    accent: g('--color-accent'),
    text: g('--color-text-primary'),
  }
}

// The two built-ins live in tokens.css and are always present.
export const BUILTIN_THEMES: ThemeOption[] = [
  { value: 'dark', label: 'Dark', base: 'dark', swatch: swatch(DARK) },
  { value: 'light', label: 'Light', base: 'light', swatch: swatch({ ...DARK, ...LIGHT }) },
]

// Eager, non-raw glob: bundles each theme's CSS into the render-blocking
// stylesheet so a custom theme paints on the first frame (no flash).
import.meta.glob('./themes/*.theme.css', { eager: true })

// Same files as raw text, to read the `@theme` metadata header at build time.
const sources = import.meta.glob('./themes/*.theme.css', {
  eager: true,
  query: '?raw',
  import: 'default',
}) as Record<string, string>

function parseTheme(path: string, source: string): ThemeOption {
  const value = path.split('/').pop()!.replace(/\.theme\.css$/, '')
  const label = /label:\s*([^\n;*]+)/.exec(source)?.[1]?.trim() || value
  const base: ThemeBase = /base:\s*light/i.test(source) ? 'light' : 'dark'
  return { value, label, base, swatch: swatch(declarations(source)) }
}

export const CUSTOM_THEMES: ThemeOption[] = Object.entries(sources)
  .map(([path, source]) => parseTheme(path, source))
  .sort((a, b) => a.label.localeCompare(b.label))

/** Built-ins first, then custom themes A→Z. Consumed by the picker. */
export const THEMES: ThemeOption[] = [...BUILTIN_THEMES, ...CUSTOM_THEMES]

/** Every valid data-theme value (excludes the meta preference 'system'). */
export const THEME_VALUES: ReadonlySet<string> = new Set(THEMES.map((t) => t.value))
