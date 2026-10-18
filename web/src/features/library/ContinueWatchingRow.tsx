import { useEffect, useRef, type PointerEvent } from 'react'
import type { LibraryEntry } from '../../api/types.ts'
import type { CardStyle } from '../../theme/cardStyle.ts'
import { ItemCard } from './ItemCard.tsx'
import { ShowCard } from './ShowCard.tsx'

// Horizontal "Continue watching" strip. Its own <section> so its cards' keys
// never collide with the grid's below (the same entries appear in both).
// Items resume; shows are spine cards at their up-next episode.
export function ContinueWatchingRow({
  entries,
  cardStyle,
  libraryPath,
}: {
  entries: LibraryEntry[]
  cardStyle: CardStyle
  libraryPath: string
}) {
  const trackRef = useRef<HTMLDivElement>(null)
  // Mouse drag-to-swipe. Touch devices get native swipe + CSS snap from
  // overflow scroll; this adds the same gesture for desktop pointers. CSS
  // snap is suspended while dragging (it fights direct scrollLeft writes).
  // Release keeps the flick's momentum: velocity is sampled during the drag,
  // the destination is projected from it, snapped to a card edge, and the row
  // decelerates there with an rAF ease-out — so a fast flick launches at the
  // hand's speed and eases in, instead of the browser's canned smooth-scroll.
  const drag = useRef({
    active: false,
    moved: false,
    startX: 0,
    startScroll: 0,
    lastX: 0,
    lastT: 0,
    velocity: 0, // px/ms, positive = scrolling right
  })
  const glide = useRef<number | null>(null)

  const stopGlide = () => {
    if (glide.current != null) {
      cancelAnimationFrame(glide.current)
      glide.current = null
    }
  }
  useEffect(() => stopGlide, [])

  const onPointerDown = (e: PointerEvent) => {
    if (e.pointerType !== 'mouse' || e.button !== 0) return
    const el = trackRef.current
    if (!el) return
    stopGlide()
    drag.current = {
      active: true,
      moved: false,
      startX: e.clientX,
      startScroll: el.scrollLeft,
      lastX: e.clientX,
      lastT: e.timeStamp,
      velocity: 0,
    }
  }

  const onPointerMove = (e: PointerEvent) => {
    const el = trackRef.current
    if (!el || !drag.current.active) return
    const dx = e.clientX - drag.current.startX
    if (!drag.current.moved && Math.abs(dx) < 4) return
    if (!drag.current.moved) {
      drag.current.moved = true
      el.style.scrollSnapType = 'none'
      el.setPointerCapture(e.pointerId)
    }
    // Exponentially-smoothed velocity: responsive to the latest hand speed but
    // not jittery, and a pause before release correctly decays it toward 0.
    const dt = e.timeStamp - drag.current.lastT
    if (dt > 0) {
      const instant = -(e.clientX - drag.current.lastX) / dt
      const blend = Math.min(1, dt / 50)
      drag.current.velocity += (instant - drag.current.velocity) * blend
    }
    drag.current.lastX = e.clientX
    drag.current.lastT = e.timeStamp
    el.scrollLeft = drag.current.startScroll - dx
  }

  const endDrag = () => {
    const el = trackRef.current
    drag.current.active = false
    if (!el) return
    if (!drag.current.moved) {
      // A press that never became a drag (e.g. a click that interrupted a
      // glide) must still hand scroll snapping back to CSS.
      el.style.scrollSnapType = ''
      return
    }
    const maxScroll = el.scrollWidth - el.clientWidth
    // Project where the flick would land under constant deceleration
    // (iOS-style: distance ∝ velocity), then settle on the card edge nearest
    // the projection. A slow release projects ~nowhere and snaps to the
    // nearest edge as before; a flick carries one or more cards further.
    const projected = Math.max(0, Math.min(maxScroll, el.scrollLeft + drag.current.velocity * 180))
    // Candidates in scroll-space (card position relative to the track, not
    // the page — offsetLeft is unreliable here because the track is not the
    // cards' offsetParent). Edges are clamped — cards near the end can't
    // reach the left edge — plus the very end of the track, so a drag near
    // the end settles flush against it instead of pulling back to a card edge.
    const trackLeft = el.getBoundingClientRect().left
    const candidates = (Array.from(el.children) as HTMLElement[]).map((card) =>
      Math.min(card.getBoundingClientRect().left - trackLeft + el.scrollLeft, maxScroll),
    )
    candidates.push(maxScroll)
    let target = 0
    let bestDist = Infinity
    for (const left of candidates) {
      const dist = Math.abs(left - projected)
      if (dist < bestDist) {
        bestDist = dist
        target = left
      }
    }

    // Glide there with an rAF tween whose initial slope matches the release
    // velocity, so the hand-off from drag to animation is seamless. Cubic
    // ease-out — fast launch, long soft landing.
    const from = el.scrollLeft
    const distance = target - from
    if (Math.abs(distance) < 1) {
      el.style.scrollSnapType = ''
      return
    }
    // Duration from velocity: a hard flick gets a longer, more cinematic
    // glide; a nudge settles quickly. Clamped so it never drags on.
    const speed = Math.abs(drag.current.velocity)
    const duration = Math.max(300, Math.min(900, 300 + speed * 250))
    let start: number | null = null
    const tick = (now: number) => {
      if (start == null) start = now
      const t = Math.min(1, (now - start) / duration)
      const eased = 1 - (1 - t) ** 3
      el.scrollLeft = from + distance * eased
      if (t < 1) {
        glide.current = requestAnimationFrame(tick)
      } else {
        glide.current = null
        // Re-enable CSS snap only after the glide settles — flipping it back
        // on mid-flight makes the browser snap instantly and kills the motion.
        el.style.scrollSnapType = ''
      }
    }
    glide.current = requestAnimationFrame(tick)
  }

  return (
    <section data-continue-watching className="mb-8">
      <h2 className="mb-3 text-lg font-semibold">Continue watching</h2>
      <div
        ref={trackRef}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={endDrag}
        onPointerCancel={endDrag}
        onClickCapture={(e) => {
          if (drag.current.moved) {
            e.preventDefault()
            e.stopPropagation()
            drag.current.moved = false
          }
        }}
        onDragStart={(e) => e.preventDefault()}
        className="scrollbar-none flex cursor-grab snap-x snap-mandatory gap-4 overflow-x-auto pb-2 select-none active:cursor-grabbing"
      >
        {entries.map((entry) => (
          <div key={`${entry.kind}:${entry.id}`} className="w-56 shrink-0 snap-start sm:w-64">
            {entry.kind === 'show' ? (
              <ShowCard show={entry} cardStyle={cardStyle} libraryPath={libraryPath} spineOnly />
            ) : (
              <ItemCard item={entry} cardStyle={cardStyle} libraryPath={libraryPath} resume />
            )}
          </div>
        ))}
      </div>
    </section>
  )
}
