import { describe, expect, it } from 'vitest'
import {
  bufferedSegments,
  clampSkip,
  hoverTime,
  isDoubleTap,
  mouseClickDecision,
  pointerRoute,
  seekDirection,
  touchTapAction,
  type BufferedSegment,
  type MouseClickDecision,
  type PointerRoute,
  type TouchTapAction,
} from './playerLogic.ts'

describe('pointerRoute', () => {
  const cases: Array<[string, PointerRoute]> = [
    ['mouse', 'mouse'],
    ['touch', 'touch'],
    ['pen', 'touch'],
    ['', 'touch'],
  ]
  it.each(cases)('routes %s → %s', (pointerType, expected) => {
    expect(pointerRoute(pointerType)).toBe(expected)
  })
})

describe('clampSkip', () => {
  const cases: Array<[string, number, number, number, number]> = [
    // name, currentTime, delta, duration, expected
    ['forward within bounds', 30, 10, 100, 40],
    ['back within bounds', 30, -10, 100, 20],
    ['clamps to duration', 95, 10, 100, 100],
    ['clamps to zero on back', 5, -10, 100, 0],
    // Before metadata: duration is NaN/0/Infinity — must not snap to 0.
    ['NaN duration keeps position', 30, 10, NaN, 40],
    ['NaN duration back clamps to 0', 5, -10, NaN, 0],
    ['zero duration keeps position', 30, 10, 0, 40],
    ['infinite duration keeps position', 30, 10, Infinity, 40],
    ['negative duration keeps position', 30, 10, -1, 40],
  ]
  it.each(cases)('%s', (_name, currentTime, delta, duration, expected) => {
    expect(clampSkip(currentTime, delta, duration)).toBe(expected)
  })
})

describe('isDoubleTap', () => {
  const cases: Array<[string, number, number, number | undefined, boolean]> = [
    ['within default threshold', 1000, 800, undefined, true],
    ['outside default threshold', 1000, 600, undefined, false],
    ['exactly at threshold is not double', 1000, 700, undefined, false],
    ['custom threshold', 1000, 800, 150, false],
    ['custom threshold hit', 1000, 900, 150, true],
  ]
  it.each(cases)('%s', (_name, now, lastTap, threshold, expected) => {
    expect(isDoubleTap(now, lastTap, threshold)).toBe(expected)
  })
})

describe('seekDirection', () => {
  const cases: Array<[string, number, number, number, -1 | 1]> = [
    ['left half seeks back', 10, 0, 100, -1],
    ['right half seeks forward', 90, 0, 100, 1],
    ['exact midpoint seeks forward', 50, 0, 100, 1],
    ['offset rect left half', 110, 100, 100, -1],
    ['offset rect right half', 190, 100, 100, 1],
  ]
  it.each(cases)('%s', (_name, clientX, rectLeft, rectWidth, expected) => {
    expect(seekDirection(clientX, rectLeft, rectWidth)).toBe(expected)
  })
})

describe('mouseClickDecision', () => {
  const cases: Array<[boolean, MouseClickDecision]> = [
    [false, 'schedule-single'],
    [true, 'double'],
  ]
  it.each(cases)('pending=%s → %s', (hasPendingSingle, expected) => {
    expect(mouseClickDecision(hasPendingSingle)).toBe(expected)
  })
})

describe('touchTapAction', () => {
  const cases: Array<[string, boolean, boolean, -1 | 1, TouchTapAction]> = [
    ['double-tap seeks back', true, false, -1, { type: 'seek', direction: -1 }],
    ['double-tap seeks forward', true, true, 1, { type: 'seek', direction: 1 }],
    ['double-tap while paused still seeks', true, true, -1, { type: 'seek', direction: -1 }],
    ['single tap paused shows controls', false, true, 1, { type: 'show-controls' }],
    ['single tap playing toggles controls', false, false, 1, { type: 'toggle-controls' }],
  ]
  it.each(cases)('%s', (_name, isDoubleTapValue, paused, direction, expected) => {
    expect(touchTapAction({ isDoubleTap: isDoubleTapValue, paused, direction })).toEqual(expected)
  })
})

describe('bufferedSegments', () => {
  const cases: Array<[string, Array<[number, number]>, number, BufferedSegment[]]> = [
    ['single leading range', [[0, 50]], 100, [{ start: 0, end: 0.5 }]],
    ['multiple disjoint ranges', [[0, 20], [40, 60]], 100, [
      { start: 0, end: 0.2 },
      { start: 0.4, end: 0.6 },
    ]],
    ['clamps overshoot to duration', [[90, 200]], 100, [{ start: 0.9, end: 1 }]],
    ['drops zero-length range', [[30, 30]], 100, []],
    ['drops inverted range', [[60, 40]], 100, []],
    ['zero duration yields nothing', [[0, 50]], 0, []],
    ['NaN duration yields nothing', [[0, 50]], NaN, []],
    ['no ranges yields nothing', [], 100, []],
  ]
  it.each(cases)('%s', (_name, ranges, duration, expected) => {
    expect(bufferedSegments(ranges, duration)).toEqual(expected)
  })
})

describe('hoverTime', () => {
  const cases: Array<[string, number, number, number, number, number]> = [
    // name, clientX, rectLeft, rectWidth, duration, expected
    ['midpoint', 50, 0, 100, 120, 60],
    ['start', 0, 0, 100, 120, 0],
    ['end', 100, 0, 100, 120, 120],
    ['clamps left of bar', -20, 0, 100, 120, 0],
    ['clamps right of bar', 300, 0, 100, 120, 120],
    ['offset rect', 150, 100, 100, 120, 60],
    ['zero width yields 0', 50, 0, 0, 120, 0],
    ['zero duration yields 0', 50, 0, 100, 0, 0],
  ]
  it.each(cases)('%s', (_name, clientX, rectLeft, rectWidth, duration, expected) => {
    expect(hoverTime(clientX, rectLeft, rectWidth, duration)).toBe(expected)
  })
})
