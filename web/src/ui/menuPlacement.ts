/* Menu placement — pure and unit-tested (Menu.test.ts). Prefers below the
   trigger; flips above when the space below can't fit and above is larger;
   clamps into the viewport with an 8 px margin. CSS anchor positioning
   isn't available everywhere yet, so coordinates are computed manually. */

export interface MenuPlacementInput {
  trigger: { top: number; bottom: number; left: number; right: number }
  menu: { width: number; height: number }
  viewport: { width: number; height: number }
  align: 'start' | 'end'
}

export interface MenuPlacement {
  top: number
  left: number
  /** Available space in the chosen direction; the menu scrolls beyond it. */
  maxHeight: number
}

const GAP = 6
const MARGIN = 8

export function placeMenu({ trigger, menu, viewport, align }: MenuPlacementInput): MenuPlacement {
  let left = align === 'end' ? trigger.right - menu.width : trigger.left
  left = Math.min(left, viewport.width - menu.width - MARGIN)
  left = Math.max(left, MARGIN)

  const below = viewport.height - trigger.bottom - GAP - MARGIN
  const above = trigger.top - GAP - MARGIN

  if (menu.height <= below || below >= above) {
    return { top: trigger.bottom + GAP, left, maxHeight: Math.max(below, 0) }
  }
  const height = Math.min(menu.height, Math.max(above, 0))
  return { top: trigger.top - GAP - height, left, maxHeight: Math.max(above, 0) }
}
