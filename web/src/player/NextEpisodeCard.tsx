import { useEffect, useRef, useState } from 'react'
import { Play } from 'lucide-react'
import { formatEpisodeLabel } from '../lib/format.ts'
import { thumbnailGenerated } from './mediaSession.ts'
import { countdownDone, countdownFraction, letterboxBars, type LibraryEpisode } from './nextEpisode.ts'

const MODIFIER_KEYS = new Set(['Shift', 'Control', 'Alt', 'Meta'])

/** The next episode's still, its label and, while counting down, a 3 px bar.
    The card is the only control: a tap plays now; a tap anywhere else, or any
    key, dismisses it and cancels the countdown. */
export default function NextEpisodeCard({
  episode,
  countdown,
  running,
  lift,
  onPlay,
  onDismiss,
}: {
  episode: LibraryEpisode
  countdown: boolean
  /** The countdown holds while playback is paused (before `ended`). */
  running: boolean
  /** Bottom offset clearing the control bar while it shows. */
  lift: number | null
  onPlay: () => void
  onDismiss: () => void
}) {
  const cardRef = useRef<HTMLButtonElement>(null)
  const [elapsed, setElapsed] = useState(0)
  const [crop, setCrop] = useState<StillCrop | null>(null)
  const fired = useRef(false)

  useEffect(() => {
    if (!countdown || !running) return
    let last = Date.now()
    const timer = window.setInterval(() => {
      const now = Date.now()
      const step = now - last
      last = now
      setElapsed((value) => value + step)
    }, 100)
    return () => window.clearInterval(timer)
  }, [countdown, running])

  useEffect(() => {
    if (!countdown || fired.current || !countdownDone(elapsed)) return
    fired.current = true
    onPlay()
  }, [countdown, elapsed, onPlay])

  useEffect(() => {
    if (!countdown) return
    const inside = (target: EventTarget | null) => target instanceof Node && cardRef.current?.contains(target)
    const onPointerDown = (event: PointerEvent) => {
      if (inside(event.target)) return
      // The dismissing tap does nothing else, so it never also pauses.
      event.stopPropagation()
      onDismiss()
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (MODIFIER_KEYS.has(event.key)) return
      if (inside(event.target) && (event.key === 'Enter' || event.key === ' ')) return
      event.stopPropagation()
      if (event.key === ' ') event.preventDefault()
      onDismiss()
    }
    window.addEventListener('pointerdown', onPointerDown, true)
    window.addEventListener('keydown', onKeyDown, true)
    return () => {
      window.removeEventListener('pointerdown', onPointerDown, true)
      window.removeEventListener('keydown', onKeyDown, true)
    }
  }, [countdown, onDismiss])

  const label = `${formatEpisodeLabel(episode.season ?? 0, episode.episode ?? 0, episode.episode_end)} · ${episode.title}`
  return (
    <button
      ref={cardRef}
      type="button"
      data-player-controls
      data-next-card
      aria-label={`Play next episode, ${label}`}
      onClick={onPlay}
      style={lift != null ? { bottom: lift } : undefined}
      className="shadow-overlay absolute right-4 bottom-6 w-[220px] cursor-pointer overflow-hidden rounded-lg border border-white/15 bg-black transition-[bottom] duration-[var(--duration-base)] sm:right-8 sm:bottom-8 sm:w-[320px]"
    >
      <span className="relative block">
        {thumbnailGenerated(episode.thumb_url) ? (
          <span className="block overflow-hidden" style={crop ? { aspectRatio: crop.aspect } : undefined}>
            <img
              src={episode.thumb_url}
              alt=""
              onLoad={(event) => setCrop(measureStill(event.currentTarget))}
              className={crop ? 'block h-full w-full object-cover' : 'block h-auto w-full'}
              style={crop ? { objectPosition: `50% ${crop.y}%` } : undefined}
            />
          </span>
        ) : (
          <span className="bg-inset block aspect-video" />
        )}
        <span className="absolute inset-x-0 bottom-0 flex items-center gap-1.5 bg-gradient-to-t from-black/85 via-black/40 to-transparent px-3 pt-8 pb-3 text-left text-sm font-medium text-white">
          <Play aria-hidden className="size-4 shrink-0" fill="currentColor" strokeWidth={1.75} />
          <span className="truncate">{label}</span>
        </span>
        {countdown && (
          <span data-countdown className="bg-progress-track absolute inset-x-0 bottom-0 h-[3px]">
            <span className="bg-accent-fill block h-full" style={{ width: `${countdownFraction(elapsed) * 100}%` }} />
          </span>
        )}
      </span>
    </button>
  )
}

type StillCrop = { aspect: number; y: number }

/** Crops a still to its picture, so a file with letterbox bars baked in
    shows no bars inside the card. */
function measureStill(img: HTMLImageElement): StillCrop | null {
  const width = 64
  const height = Math.max(1, Math.round((img.naturalHeight / img.naturalWidth) * width))
  const canvas = document.createElement('canvas')
  canvas.width = width
  canvas.height = height
  const context = canvas.getContext('2d', { willReadFrequently: true })
  if (!context || !img.naturalWidth) return null
  context.drawImage(img, 0, 0, width, height)
  const { data } = context.getImageData(0, 0, width, height)
  const rows: number[] = []
  for (let y = 0; y < height; y++) {
    let sum = 0
    for (let x = 0; x < width; x++) {
      const i = (y * width + x) * 4
      sum += 0.2126 * data[i] + 0.7152 * data[i + 1] + 0.0722 * data[i + 2]
    }
    rows.push(sum / width)
  }
  const bars = letterboxBars(rows)
  const barsTotal = bars.top + bars.bottom
  const aspect = img.naturalWidth / (img.naturalHeight * (1 - barsTotal))
  return { aspect, y: barsTotal > 0 ? (bars.top / barsTotal) * 100 : 50 }
}
