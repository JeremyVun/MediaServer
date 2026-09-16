import { useEffect, useState, type RefObject } from 'react'
import { formatClock } from '../lib/format.ts'
import { bufferedSegments, hoverTime } from './playerLogic.ts'

// The seek bar and clock own the video's current time so its 4 Hz `timeupdate`
// re-renders only these two small components, not the whole player.

function useVideoTime(videoRef: RefObject<HTMLVideoElement | null>): number {
  const [currentTime, setCurrentTime] = useState(0)
  useEffect(() => {
    const video = videoRef.current
    if (!video) return
    const sync = () => setCurrentTime(video.currentTime)
    video.addEventListener('timeupdate', sync)
    video.addEventListener('seeking', sync)
    return () => {
      video.removeEventListener('timeupdate', sync)
      video.removeEventListener('seeking', sync)
    }
  }, [videoRef])
  return currentTime
}

// Buffered TimeRanges flattened to [start, end] second pairs; the seek bar
// renders them as a lighter segment behind the played portion.
function useBufferedRanges(videoRef: RefObject<HTMLVideoElement | null>): Array<[number, number]> {
  const [buffered, setBuffered] = useState<Array<[number, number]>>([])
  useEffect(() => {
    const video = videoRef.current
    if (!video) return
    const read = () => {
      const ranges: Array<[number, number]> = []
      for (let i = 0; i < video.buffered.length; i++) {
        ranges.push([video.buffered.start(i), video.buffered.end(i)])
      }
      setBuffered(ranges)
    }
    const clear = () => setBuffered([])
    video.addEventListener('progress', read)
    video.addEventListener('emptied', clear)
    return () => {
      video.removeEventListener('progress', read)
      video.removeEventListener('emptied', clear)
    }
  }, [videoRef])
  return buffered
}

export function Clock({ videoRef, duration }: { videoRef: RefObject<HTMLVideoElement | null>; duration: number }) {
  const currentTime = useVideoTime(videoRef)
  return (
    <span className="tabular min-w-[104px] text-sm text-secondary">
      {formatClock(currentTime)} / {formatClock(duration)}
    </span>
  )
}

export function SeekBar({
  videoRef,
  duration,
  coarsePointer,
}: {
  videoRef: RefObject<HTMLVideoElement | null>
  duration: number
  coarsePointer: boolean
}) {
  const currentTime = useVideoTime(videoRef)
  const buffered = useBufferedRanges(videoRef)
  // Desktop-only timestamp tooltip at the hovered seek-bar position.
  const [hoverPreview, setHoverPreview] = useState<{ ratio: number; time: number } | null>(null)

  // Custom seek bar per DESIGN-SYSTEM player chrome: 4px base track, buffered
  // ranges lighter behind an amber played range, thumb on hover/drag. The
  // native range input stays on top (transparent track, styled thumb) so
  // keyboard/ARIA slider semantics are unchanged.
  return (
    <div
      className="relative mb-3 h-6"
      onPointerMove={(e) => {
        if (coarsePointer || !duration) return
        const rect = e.currentTarget.getBoundingClientRect()
        const ratio = rect.width > 0 ? Math.max(0, Math.min(1, (e.clientX - rect.left) / rect.width)) : 0
        setHoverPreview({ ratio, time: hoverTime(e.clientX, rect.left, rect.width, duration) })
      }}
      onPointerLeave={() => setHoverPreview(null)}
    >
      <div
        className="pointer-events-none absolute inset-x-0 top-1/2 h-1 -translate-y-1/2 overflow-hidden rounded-full"
        style={{ backgroundColor: 'rgb(255 255 255 / 0.25)' }}
      >
        {bufferedSegments(buffered, duration).map((segment, index) => (
          <div
            key={index}
            className="absolute inset-y-0"
            style={{
              left: `${segment.start * 100}%`,
              width: `${(segment.end - segment.start) * 100}%`,
              backgroundColor: 'rgb(255 255 255 / 0.45)',
            }}
          />
        ))}
        <div
          className="bg-accent-fill absolute inset-y-0 left-0"
          style={{ width: `${duration > 0 ? Math.min(1, currentTime / duration) * 100 : 0}%` }}
        />
      </div>
      <input
        type="range"
        min={0}
        max={duration || 0}
        step={0.1}
        value={Math.min(currentTime, duration || currentTime)}
        onChange={(e) => {
          const video = videoRef.current
          if (!video) return
          video.currentTime = Number(e.currentTarget.value)
        }}
        aria-label="Seek"
        className="relative z-10 block h-6 w-full cursor-pointer appearance-none bg-transparent focus-visible:outline-none [&::-moz-range-thumb]:size-3.5 [&::-moz-range-thumb]:appearance-none [&::-moz-range-thumb]:rounded-full [&::-moz-range-thumb]:border-0 [&::-moz-range-thumb]:bg-white [&::-moz-range-thumb]:opacity-0 [&::-moz-range-thumb]:transition-opacity [&::-moz-range-track]:h-1 [&::-moz-range-track]:rounded-full [&::-moz-range-track]:bg-transparent [&::-webkit-slider-runnable-track]:h-1 [&::-webkit-slider-runnable-track]:rounded-full [&::-webkit-slider-runnable-track]:bg-transparent [&::-webkit-slider-thumb]:mt-[-5px] [&::-webkit-slider-thumb]:size-3.5 [&::-webkit-slider-thumb]:appearance-none [&::-webkit-slider-thumb]:rounded-full [&::-webkit-slider-thumb]:bg-white [&::-webkit-slider-thumb]:opacity-0 [&::-webkit-slider-thumb]:transition-opacity hover:[&::-moz-range-thumb]:opacity-100 hover:[&::-webkit-slider-thumb]:opacity-100 focus-visible:[&::-moz-range-thumb]:opacity-100 focus-visible:[&::-webkit-slider-thumb]:opacity-100 active:[&::-moz-range-thumb]:opacity-100 active:[&::-webkit-slider-thumb]:opacity-100"
      />
      {hoverPreview && (
        <div
          className="bg-raised/90 text-primary shadow-overlay tabular pointer-events-none absolute bottom-7 -translate-x-1/2 rounded-sm px-2 py-1 text-xs"
          style={{ left: `${hoverPreview.ratio * 100}%` }}
        >
          {formatClock(hoverPreview.time)}
        </div>
      )}
    </div>
  )
}
