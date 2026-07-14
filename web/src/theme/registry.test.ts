import { describe, expect, it } from 'vitest'
import { BUILTIN_THEMES, THEMES, THEME_VALUES } from './registry.ts'

// Guards the swatch parse in registry.ts. Its DARK/LIGHT extraction regexes are
// coupled to the punctuation of tokens.css; a benign reformat (quote style,
// selector split, a dropped trailing ';') could silently make a regex miss and
// render every picker swatch as the '#000000' fallback with no build error. The
// contrast test parses the CSS itself, so it would not catch that — this does.
describe('theme registry swatches', () => {
  const dark = BUILTIN_THEMES.find((t) => t.value === 'dark')!
  const light = BUILTIN_THEMES.find((t) => t.value === 'light')!

  it('parses the built-in dark canvas from tokens.css', () => {
    expect(dark.swatch.canvas).toBe('#0c0d10')
  })

  it('parses the built-in light canvas from tokens.css', () => {
    expect(light.swatch.canvas).toBe('#f7f6f3')
  })

  it('gives every theme four hex swatch colours', () => {
    for (const theme of THEMES) {
      for (const key of ['canvas', 'surface', 'accent', 'text'] as const) {
        expect(theme.swatch[key], `${theme.value}.${key}`).toMatch(/^#[0-9a-fA-F]{3,8}$/)
      }
    }
  })

  it('discovers the built-in and custom theme values', () => {
    for (const value of ['dark', 'light', 'netflix', 'crisp', 'spotify', 'nord', 'dracula']) {
      expect(THEME_VALUES.has(value)).toBe(true)
    }
  })
})
