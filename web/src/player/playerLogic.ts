// Pure, unit-testable decision logic for the player's pointer handling and
// seek clamping. Kept free of React/DOM so it can be table-tested (see
// playerLogic.test.ts). Player.tsx wires these to real events and refs.

export type PointerRoute = 'mouse' | 'touch'

// Mouse gets desktop conventions (click = play/pause, double-click =
// fullscreen). Touch and pen keep the tap-toggles-controls / double-tap-seek
// behavior.
export function pointerRoute(pointerType: string): PointerRoute {
  return pointerType === 'mouse' ? 'mouse' : 'touch'
}

// Clamp a relative skip. When duration is a finite positive number we clamp to
// [0, duration]; before metadata (duration NaN/0/Infinity) we clamp only to a
// non-negative lower bound so an early skip doesn't snap the position to 0.
export function clampSkip(currentTime: number, delta: number, duration: number): number {
  const next = currentTime + delta
  if (Number.isFinite(duration) && duration > 0) {
    return Math.max(0, Math.min(duration, next))
  }
  return Math.max(0, next)
}

// A tap is a double-tap when it lands within `threshold` ms of the previous one.
export function isDoubleTap(now: number, lastTap: number, threshold = 300): boolean {
  return now - lastTap < threshold
}

// Which half of the surface was tapped: left half seeks back (-1), right half
// seeks forward (+1).
export function seekDirection(clientX: number, rectLeft: number, rectWidth: number): -1 | 1 {
  return clientX < rectLeft + rectWidth / 2 ? -1 : 1
}

// Mouse single/double click discrimination. Each click either schedules a
// single-click action (play/pause) after a short timer, or — if a single-click
// timer is already pending — resolves as a double click (fullscreen), cancelling
// the pending single.
export type MouseClickDecision = 'schedule-single' | 'double'

export function mouseClickDecision(hasPendingSingle: boolean): MouseClickDecision {
  return hasPendingSingle ? 'double' : 'schedule-single'
}

// Touch/pen tap dispatch: double-tap seeks in the tapped direction; a single
// tap shows controls while paused, otherwise toggles them.
export type TouchTapAction =
  | { type: 'seek'; direction: -1 | 1 }
  | { type: 'show-controls' }
  | { type: 'toggle-controls' }

export function touchTapAction(params: {
  isDoubleTap: boolean
  paused: boolean
  direction: -1 | 1
}): TouchTapAction {
  if (params.isDoubleTap) return { type: 'seek', direction: params.direction }
  if (params.paused) return { type: 'show-controls' }
  return { type: 'toggle-controls' }
}

// A buffered span expressed as fractions [0, 1] of the total duration, ready to
// position as a seek-bar segment (left/width percentages).
export type BufferedSegment = { start: number; end: number }

// Convert a video's `buffered` TimeRanges (passed as [start, end] second pairs)
// into duration-relative segments. Ranges are clamped to [0, duration];
// zero-length or inverted spans and a non-positive/non-finite duration yield no
// segments so the seek bar simply shows nothing buffered rather than NaN widths.
export function bufferedSegments(ranges: Array<[number, number]>, duration: number): BufferedSegment[] {
  if (!Number.isFinite(duration) || duration <= 0) return []
  const segments: BufferedSegment[] = []
  for (const [rawStart, rawEnd] of ranges) {
    const start = Math.max(0, Math.min(duration, rawStart))
    const end = Math.max(0, Math.min(duration, rawEnd))
    if (!(end > start)) continue
    segments.push({ start: start / duration, end: end / duration })
  }
  return segments
}

// Map a horizontal pointer position over the seek bar to a timestamp in
// seconds, clamped to [0, duration]. Used for the desktop hover tooltip; a
// non-positive width or duration yields 0.
export function hoverTime(clientX: number, rectLeft: number, rectWidth: number, duration: number): number {
  if (rectWidth <= 0 || !Number.isFinite(duration) || duration <= 0) return 0
  const fraction = Math.max(0, Math.min(1, (clientX - rectLeft) / rectWidth))
  return fraction * duration
}
