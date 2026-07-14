import { describe, expect, it } from 'vitest'
import tokensCss from './tokens.css?raw'

// Every custom theme's source, read at build time.
const themeSources = import.meta.glob('./themes/*.theme.css', {
  eager: true,
  query: '?raw',
  import: 'default',
}) as Record<string, string>

type Tokens = Record<string, string>

// Extract `--name: value;` declarations from a CSS block body.
function declarations(body: string): Tokens {
  const out: Tokens = {}
  const re = /(--[\w-]+)\s*:\s*([^;]+);/g
  let m: RegExpExecArray | null
  while ((m = re.exec(body))) out[m[1]] = m[2].trim()
  return out
}

// Body of the first rule whose selector matches.
function block(css: string, selector: RegExp): Tokens {
  const m = selector.exec(css)
  return m ? declarations(m[1]) : {}
}

// The dark block is the baseline every non-'light' theme cascades over:
// `[data-theme='light']` never matches a custom theme name, so a custom
// theme inherits DARK for anything it doesn't override.
const DARK = block(tokensCss, /:root,\s*\[data-theme='dark'\]\s*\{([^}]*)\}/)
const LIGHT_OVERRIDES = block(tokensCss, /\[data-theme='light'\]\s*\{([^}]*)\}/)

function hexToRgb(hex: string): [number, number, number] {
  const m = /^#([0-9a-f]{3}|[0-9a-f]{6})$/i.exec(hex.trim())
  if (!m) throw new Error(`expected a hex colour, got: ${hex}`)
  const h = m[1].length === 3 ? m[1].replace(/(.)/g, '$1$1') : m[1]
  return [parseInt(h.slice(0, 2), 16), parseInt(h.slice(2, 4), 16), parseInt(h.slice(4, 6), 16)]
}

function luminance(hex: string): number {
  const lin = (v: number) => {
    const c = v / 255
    return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4)
  }
  const [r, g, b] = hexToRgb(hex)
  return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b)
}

function contrast(a: string, b: string): number {
  const la = luminance(a)
  const lb = luminance(b)
  const hi = Math.max(la, lb)
  const lo = Math.min(la, lb)
  return (hi + 0.05) / (lo + 0.05)
}

interface ThemeCase {
  value: string
  overrides: Tokens
}

// Built-ins are validated too — they double as a self-check on the harness.
const cases: ThemeCase[] = [
  { value: 'dark', overrides: {} },
  { value: 'light', overrides: LIGHT_OVERRIDES },
  ...Object.entries(themeSources).map(([path, src]) => ({
    value: path.split('/').pop()!.replace(/\.theme\.css$/, ''),
    overrides: declarations(src),
  })),
]

const AA = 4.5
const BACKGROUNDS = ['--color-bg-canvas', '--color-bg-surface', '--color-bg-raised'] as const
const TEXTS = ['--color-text-primary', '--color-text-secondary'] as const

describe('theme contrast (WCAG AA)', () => {
  for (const { value, overrides } of cases) {
    const t: Tokens = { ...DARK, ...overrides }
    describe(value, () => {
      for (const text of TEXTS) {
        for (const bg of BACKGROUNDS) {
          it(`${text} on ${bg} ≥ ${AA}:1`, () => {
            expect(contrast(t[text], t[bg])).toBeGreaterThanOrEqual(AA)
          })
        }
      }
      it(`text-on-accent on accent-fill ≥ ${AA}:1`, () => {
        expect(contrast(t['--color-text-on-accent'], t['--color-accent-fill'])).toBeGreaterThanOrEqual(AA)
      })
    })
  }
})
