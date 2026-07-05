import { describe, expect, it } from 'vitest'
import { placeMenu, type MenuPlacementInput } from './menuPlacement.ts'

// Trigger: a 36×36 button; viewport: 800×600 unless a case says otherwise.
const base: MenuPlacementInput = {
  trigger: { top: 100, bottom: 136, left: 400, right: 436 },
  menu: { width: 192, height: 200 },
  viewport: { width: 800, height: 600 },
  align: 'end',
}

describe('placeMenu', () => {
  const cases: Array<{ name: string; input: MenuPlacementInput; expected: Partial<ReturnType<typeof placeMenu>> }> = [
    {
      name: 'opens below, end-aligned to the trigger',
      input: base,
      expected: { top: 142, left: 244 },
    },
    {
      name: 'start alignment uses the trigger left edge',
      input: { ...base, align: 'start' },
      expected: { top: 142, left: 400 },
    },
    {
      name: 'clamps to the left viewport margin (leftmost tile symptom)',
      input: { ...base, trigger: { top: 100, bottom: 136, left: 10, right: 46 } },
      expected: { left: 8 },
    },
    {
      name: 'clamps to the right viewport margin',
      input: { ...base, align: 'start', trigger: { top: 100, bottom: 136, left: 700, right: 736 } },
      expected: { left: 800 - 192 - 8 },
    },
    {
      name: 'flips above when below is too small and above is larger',
      input: { ...base, trigger: { top: 500, bottom: 536, left: 400, right: 436 } },
      // above the trigger: top = 500 - 6 - 200
      expected: { top: 294, maxHeight: 500 - 6 - 8 },
    },
    {
      name: 'stays below when neither side fits but below is larger',
      input: { ...base, menu: { width: 192, height: 900 }, trigger: { top: 100, bottom: 136, left: 400, right: 436 } },
      expected: { top: 142, maxHeight: 600 - 136 - 6 - 8 },
    },
    {
      name: 'flipped menu taller than the space above pins to the margin',
      input: { ...base, menu: { width: 192, height: 900 }, trigger: { top: 500, bottom: 590, left: 400, right: 436 } },
      // below = 600-590-6-8 = -4, above = 500-6-8 = 486 → height capped at 486
      expected: { top: 8, maxHeight: 486 },
    },
    {
      name: 'menu wider than the viewport pins to the left margin',
      input: { ...base, menu: { width: 900, height: 200 }, viewport: { width: 400, height: 600 } },
      expected: { left: 8 },
    },
  ]

  it.each(cases)('$name', ({ input, expected }) => {
    const placed = placeMenu(input)
    for (const [key, value] of Object.entries(expected)) {
      expect(placed[key as keyof typeof placed]).toBe(value)
    }
  })

  it('never returns a negative maxHeight', () => {
    const placed = placeMenu({
      ...base,
      trigger: { top: 596, bottom: 600, left: 0, right: 36 },
      menu: { width: 192, height: 400 },
    })
    expect(placed.maxHeight).toBeGreaterThanOrEqual(0)
  })
})
