import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type PointerEvent as ReactPointerEvent,
} from 'react'
import type Hls from 'hls.js'
import { useLocation, useNavigate, useParams, useSearchParams } from 'react-router'
import {
  ArrowLeft,
  Cast,
  Captions,
  ChevronDown,
  Loader2,
  Maximize,
  Minimize,
  Pause,
  Play,
  RotateCcw,
  RotateCw,
  Volume2,
  VolumeX,
  X,
} from 'lucide-react'
import {
  beaconPlaybackSession,
  deletePlaybackSession,
  saveProgressBeacon,
  useItem,
  useSaveProgress,
} from '../api/queries.ts'
import type { MediaStream } from '../api/types.ts'
import { formatClock } from '../lib/format.ts'
import { parseResumeOverride } from '../lib/searchParams.ts'
import { Button, IconButton, Menu, MenuItem } from '../ui/index.ts'
import { usePlaybackSession } from './usePlaybackSession.ts'
import {
  bufferedSegments,
  clampSkip,
  hoverTime,
  isDoubleTap,
  mouseClickDecision,
  pointerRoute,
  seekDirection,
  touchTapAction,
} from './playerLogic.ts'

type AirPlayVideo = HTMLVideoElement & {
  webkitShowPlaybackTargetPicker?: () => void
}

// iOS Safari does not implement Element.requestFullscreen; it exposes vendor
// fullscreen on the container and a native video-fullscreen on the element.
type FullscreenContainer = HTMLDivElement & {
  webkitRequestFullscreen?: () => void
}

type FullscreenVideo = HTMLVideoElement & {
  webkitEnterFullscreen?: () => void
  webkitExitFullscreen?: () => void
}

type FullscreenDocument = Document & {
  webkitFullscreenElement?: Element | null
  webkitExitFullscreen?: () => void
}

// A seek badge flashed on double-tap; `nonce` restarts the fade timer.
type SeekIndicator = { direction: -1 | 1; seconds: number; nonce: number }

export function PlayerPage() {
  const params = useParams()
  const [searchParams] = useSearchParams()
  const itemID = numeric(params.id)
  const fileID = numeric(searchParams.get('file_id'))

  if (itemID == null) {
    return (
      <main data-theme="dark" className="flex min-h-screen items-center justify-center bg-canvas text-primary">
        <p className="text-lg font-semibold">Item not found</p>
      </main>
    )
  }

  return <Player itemID={itemID} fileID={fileID} />
}

function Player({ itemID, fileID }: { itemID: number; fileID: number | null }) {
  const navigate = useNavigate()
  const location = useLocation()
  // Where "back" goes: the page that linked here (passed via router state), or
  // the library home for entries that skip the detail page (e.g. a poster's
  // play tile, or "Continue watching") and have no such state.
  const cameFromApp = (location.state as { from?: string } | null)?.from != null
  const backTo = (location.state as { from?: string } | null)?.from ?? '/'
  // When the source page is in the history stack, go *back* to it rather than
  // pushing a fresh entry — the original entry's location.key is what the
  // library's per-entry scroll restoration is keyed on. A push would mint a
  // new key and land the user at the top of the page.
  const goBack = useCallback(() => {
    if (cameFromApp) navigate(-1)
    else navigate(backTo)
  }, [cameFromApp, backTo, navigate])
  const [searchParams] = useSearchParams()
  // 3.2: "Play" appends ?t=0 (or &t=0) to force start-from-zero; a present,
  // valid, non-negative `t` overrides the stored-progress resume position.
  const resumeOverride = parseResumeOverride(searchParams.get('t'))
  const containerRef = useRef<HTMLDivElement>(null)
  const videoRef = useRef<HTMLVideoElement>(null)
  const hlsRef = useRef<Hls | null>(null)
  const lastTap = useRef(0)
  const resumed = useRef(false)
  const pendingSeek = useRef<number | null>(null)
  const hideTimer = useRef<number | null>(null)
  const singleClickTimer = useRef<number | null>(null)
  const bufferingTimer = useRef<number | null>(null)
  const hoveringControls = useRef(false)
  const menuOpen = useRef(false)
  // Read in the long-lived keydown listener so it never re-subscribes just to
  // see whether the help sheet is open.
  const helpOpenRef = useRef(false)
  const item = useItem(itemID)
  const [selectedSubtitleIndex, setSelectedSubtitleIndex] = useState<number | null>(null)
  const activeFile = useMemo(() => {
    const files = item.data?.files ?? []
    if (fileID != null) return files.find((file) => file.id === fileID) ?? null
    return files.find((file) => file.status === 'online') ?? files[0] ?? null
  }, [fileID, item.data?.files])
  const subtitleOptions = useMemo(
    () => activeFile?.streams.filter((stream) => stream.kind === 'subtitle') ?? [],
    [activeFile],
  )
  const selectedSubtitle = useMemo(
    () => subtitleOptions.find((stream) => stream.stream_index === selectedSubtitleIndex) ?? null,
    [selectedSubtitleIndex, subtitleOptions],
  )
  const burnInSubtitleIndex =
    selectedSubtitle && isImageSubtitleCodec(selectedSubtitle.codec) ? selectedSubtitle.stream_index : null
  const textSubtitleIndex =
    selectedSubtitle && isTextSubtitleCodec(selectedSubtitle.codec) ? selectedSubtitle.stream_index : null
  const session = usePlaybackSession(itemID, fileID, burnInSubtitleIndex)
  const { mutate: saveProgressMutate } = useSaveProgress(itemID)

  const [paused, setPaused] = useState(true)
  const [muted, setMuted] = useState(false)
  const [volume, setVolume] = useState(1)
  const [duration, setDuration] = useState(0)
  const [currentTime, setCurrentTime] = useState(0)
  const [playbackRate, setPlaybackRate] = useState(1)
  const [controlsVisible, setControlsVisible] = useState(true)
  const [fullscreen, setFullscreen] = useState(false)
  const [videoError, setVideoError] = useState(false)
  const [seekIndicator, setSeekIndicator] = useState<SeekIndicator | null>(null)
  // Buffered TimeRanges flattened to [start, end] second pairs; the seek bar
  // renders them as a lighter segment behind the played portion.
  const [buffered, setBuffered] = useState<Array<[number, number]>>([])
  // Spinner shown on `waiting` (playback stalled for lack of buffered frames),
  // debounced so routine seeks don't flash it. Deliberately NOT on `stalled`:
  // that's a network-idle event that fires during healthy playback once the
  // buffer fills, so it wrongly sticks the spinner on until the next pause/play.
  const [buffering, setBuffering] = useState(false)
  // Desktop-only timestamp tooltip at the hovered seek-bar position.
  const [hoverPreview, setHoverPreview] = useState<{ ratio: number; time: number } | null>(null)
  const [helpOpen, setHelpOpen] = useState(false)
  const [airplaySupported] = useState(
    () => typeof HTMLVideoElement !== 'undefined' && 'webkitShowPlaybackTargetPicker' in HTMLVideoElement.prototype,
  )
  // iOS ignores programmatic video.volume, so hide the slider on coarse pointers.
  const [coarsePointer] = useState(
    () => typeof window !== 'undefined' && window.matchMedia?.('(pointer: coarse)').matches === true,
  )

  // Last observed playback position, refreshed on every timeupdate. The
  // unmount save (back navigation) runs after React has already detached
  // videoRef, so it must read from here — reading the <video> element at that
  // point returns nothing and the final position would be silently dropped.
  const lastProgress = useRef<{ position_s: number; duration_s: number } | null>(null)

  const progressPayload = useCallback(() => {
    const video = videoRef.current
    if (video && Number.isFinite(video.duration) && video.duration > 0) {
      lastProgress.current = {
        position_s: video.currentTime,
        duration_s: video.duration,
      }
    }
    return lastProgress.current
  }, [])

  const persistProgress = useCallback(
    (beacon = false) => {
      const payload = progressPayload()
      if (!payload) return
      if (beacon) {
        saveProgressBeacon(itemID, payload)
      } else {
        saveProgressMutate(payload)
      }
    },
    [itemID, progressPayload, saveProgressMutate],
  )

  // Navigating between items reuses this component; the old item's final
  // position has already been flushed by the effect cleanup below, so drop it
  // before the new item's first save can pick it up.
  useEffect(() => {
    lastProgress.current = null
  }, [itemID])

  // paused mirrored in a ref so the self-rescheduling hide tick reads the live
  // value without being torn down and rebuilt on every play/pause.
  const pausedRef = useRef(paused)
  useEffect(() => {
    pausedRef.current = paused
  }, [paused])

  // Hide timer lives in a ref (1.5): every activity clears and re-arms it, and
  // it never hides while paused, while the pointer hovers a control bar, or
  // while a control-bar menu is open — in those cases it just re-arms.
  const armHideTimer = useCallback(() => {
    if (hideTimer.current != null) window.clearTimeout(hideTimer.current)
    const tick = () => {
      if (pausedRef.current || hoveringControls.current || menuOpen.current) {
        hideTimer.current = window.setTimeout(tick, 3000)
        return
      }
      hideTimer.current = null
      setControlsVisible(false)
    }
    hideTimer.current = window.setTimeout(tick, 3000)
  }, [])

  const registerActivity = useCallback(() => {
    setControlsVisible(true)
    armHideTimer()
  }, [armHideTimer])

  const togglePlay = useCallback(() => {
    const video = videoRef.current
    if (!video) return
    if (paused) {
      void video.play()
    } else {
      video.pause()
    }
  }, [paused])

  const skip = useCallback((delta: number) => {
    const video = videoRef.current
    if (!video) return
    video.currentTime = clampSkip(video.currentTime, delta, video.duration)
  }, [])

  const readBuffered = useCallback((video: HTMLVideoElement) => {
    const ranges: Array<[number, number]> = []
    for (let i = 0; i < video.buffered.length; i++) {
      ranges.push([video.buffered.start(i), video.buffered.end(i)])
    }
    setBuffered(ranges)
  }, [])

  // Debounce the spinner ~250ms so a routine seek (which briefly fires
  // `waiting`) doesn't flash it; `canplay`/`playing`/`timeupdate` clear it.
  const showBuffering = useCallback(() => {
    if (bufferingTimer.current != null) return
    bufferingTimer.current = window.setTimeout(() => {
      bufferingTimer.current = null
      setBuffering(true)
    }, 250)
  }, [])

  const clearBuffering = useCallback(() => {
    if (bufferingTimer.current != null) {
      window.clearTimeout(bufferingTimer.current)
      bufferingTimer.current = null
    }
    setBuffering(false)
  }, [])

  const flashSeek = useCallback((direction: -1 | 1) => {
    setSeekIndicator((prev) => ({
      direction,
      // Accumulate when re-tapping the same direction within the fade window.
      seconds: prev && prev.direction === direction ? prev.seconds + 10 : 10,
      nonce: (prev?.nonce ?? 0) + 1,
    }))
  }, [])

  const changeVolume = useCallback((delta: number) => {
    const video = videoRef.current
    if (!video) return
    const next = Math.max(0, Math.min(1, video.volume + delta))
    video.volume = next
    video.muted = next === 0
  }, [])

  const toggleFullscreen = useCallback(() => {
    const container = containerRef.current as FullscreenContainer | null
    const video = videoRef.current as FullscreenVideo | null
    if (!container) return
    const doc = document as FullscreenDocument
    const active = document.fullscreenElement ?? doc.webkitFullscreenElement
    if (active) {
      if (document.exitFullscreen) void document.exitFullscreen()
      else if (doc.webkitExitFullscreen) doc.webkitExitFullscreen()
      else video?.webkitExitFullscreen?.()
      return
    }
    if (container.requestFullscreen) void container.requestFullscreen()
    else if (container.webkitRequestFullscreen) container.webkitRequestFullscreen()
    else video?.webkitEnterFullscreen?.() // iOS-native video fullscreen
  }, [])

  const showAirPlay = useCallback(() => {
    const video = videoRef.current as AirPlayVideo | null
    video?.webkitShowPlaybackTargetPicker?.()
  }, [])

  const selectSubtitle = useCallback(
    (streamIndex: number | null) => {
      const next = subtitleOptions.find((stream) => stream.stream_index === streamIndex) ?? null
      const currentBurnIn = selectedSubtitle != null && isImageSubtitleCodec(selectedSubtitle.codec)
      const nextBurnIn = next != null && isImageSubtitleCodec(next.codec)
      if (currentBurnIn || nextBurnIn) {
        const video = videoRef.current
        if (video && Number.isFinite(video.currentTime)) pendingSeek.current = video.currentTime
      }
      setSelectedSubtitleIndex(streamIndex)
    },
    [selectedSubtitle, subtitleOptions],
  )

  const cycleSubtitles = useCallback(() => {
    if (subtitleOptions.length === 0) return
    const current = subtitleOptions.findIndex((stream) => stream.stream_index === selectedSubtitleIndex)
    const next = current < 0 ? 0 : current + 1
    selectSubtitle(next >= subtitleOptions.length ? null : subtitleOptions[next].stream_index)
  }, [selectSubtitle, selectedSubtitleIndex, subtitleOptions])

  useEffect(() => {
    const video = videoRef.current
    if (!video || !session.data) return
    let cancelled = false
    hlsRef.current?.destroy()
    hlsRef.current = null
    setVideoError(false)
    resumed.current = false
    video.removeAttribute('src')

    if (session.data.mode === 'direct' || session.capabilities.native_hls) {
      video.src = session.data.url
      video.load()
      return () => {
        cancelled = true
        video.removeAttribute('src')
        video.load()
      }
    }

    void import('hls.js')
      .then(({ default: Hls }) => {
        if (cancelled || !Hls.isSupported()) {
          if (!cancelled) setVideoError(true)
          return
        }
        const hls = new Hls()
        hlsRef.current = hls
        hls.on(Hls.Events.ERROR, (_event, data) => {
          if (!data.fatal) return
          if (data.type === Hls.ErrorTypes.NETWORK_ERROR) {
            hls.startLoad()
            return
          }
          if (data.type === Hls.ErrorTypes.MEDIA_ERROR) {
            hls.recoverMediaError()
            return
          }
          setVideoError(true)
        })
        hls.loadSource(session.data.url)
        hls.attachMedia(video)
      })
      .catch(() => {
        if (!cancelled) setVideoError(true)
      })

    return () => {
      cancelled = true
      hlsRef.current?.destroy()
      hlsRef.current = null
      video.removeAttribute('src')
      video.load()
    }
    // session.dataUpdatedAt changes on every fetch, including a refetch that
    // resolves to a structurally-shared (reference-equal) `data` — needed so
    // the Retry button actually reloads the element for direct-play sessions.
  }, [session.capabilities.native_hls, session.data, session.dataUpdatedAt])

  useEffect(() => {
    const sessionID = session.data?.session_id
    if (!sessionID) return
    const onPageHide = () => beaconPlaybackSession(sessionID)
    window.addEventListener('pagehide', onPageHide)
    return () => {
      window.removeEventListener('pagehide', onPageHide)
      deletePlaybackSession(sessionID)
    }
  }, [session.data?.session_id])

  useEffect(() => {
    const video = videoRef.current
    if (!video) return
    const tracks = video.querySelectorAll<HTMLTrackElement>('track[data-stream-index]')
    tracks.forEach((track) => {
      const index = Number(track.dataset.streamIndex)
      track.track.mode = index === textSubtitleIndex ? 'showing' : 'disabled'
    })
  }, [session.data?.subtitles, textSubtitleIndex])

  useEffect(() => {
    if (paused) return
    const timer = window.setInterval(() => persistProgress(false), 10_000)
    return () => window.clearInterval(timer)
  }, [paused, persistProgress])

  useEffect(() => {
    const onVisibility = () => {
      if (document.visibilityState === 'hidden') persistProgress(true)
    }
    const onPageHide = () => persistProgress(true)
    document.addEventListener('visibilitychange', onVisibility)
    window.addEventListener('pagehide', onPageHide)
    return () => {
      document.removeEventListener('visibilitychange', onVisibility)
      window.removeEventListener('pagehide', onPageHide)
      persistProgress(true)
    }
  }, [persistProgress])

  // Keep controls up (and the hide timer stopped) while paused; arm the timer
  // once playback resumes. All other activity re-arms via registerActivity.
  useEffect(() => {
    if (paused) {
      // onPause already surfaces the controls; here we just stop the timer.
      if (hideTimer.current != null) window.clearTimeout(hideTimer.current)
      hideTimer.current = null
    } else {
      armHideTimer()
    }
  }, [paused, armHideTimer])

  // Clear any dangling timers on unmount.
  useEffect(
    () => () => {
      if (hideTimer.current != null) window.clearTimeout(hideTimer.current)
      if (singleClickTimer.current != null) window.clearTimeout(singleClickTimer.current)
      if (bufferingTimer.current != null) window.clearTimeout(bufferingTimer.current)
    },
    [],
  )

  useEffect(() => {
    helpOpenRef.current = helpOpen
  }, [helpOpen])

  // Fade the double-tap seek badge ~600ms after the latest tap (nonce re-arms).
  useEffect(() => {
    if (!seekIndicator) return
    const timer = window.setTimeout(() => setSeekIndicator(null), 600)
    return () => window.clearTimeout(timer)
  }, [seekIndicator])

  useEffect(() => {
    const onFullscreen = () => {
      const doc = document as FullscreenDocument
      const active = document.fullscreenElement ?? doc.webkitFullscreenElement
      setFullscreen(active === containerRef.current)
    }
    const video = videoRef.current
    const onEndFullscreen = () => setFullscreen(false)
    document.addEventListener('fullscreenchange', onFullscreen)
    document.addEventListener('webkitfullscreenchange', onFullscreen)
    video?.addEventListener('webkitendfullscreen', onEndFullscreen)
    return () => {
      document.removeEventListener('fullscreenchange', onFullscreen)
      document.removeEventListener('webkitfullscreenchange', onFullscreen)
      video?.removeEventListener('webkitendfullscreen', onEndFullscreen)
    }
  }, [])

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.metaKey || event.ctrlKey || event.altKey) return
      // A control-bar Menu owns keyboard nav (arrows/enter/space) while open —
      // don't also fire player shortcuts.
      if (menuOpen.current) return
      const target = event.target
      if (target instanceof HTMLInputElement || target instanceof HTMLSelectElement) return
      if (target instanceof HTMLTextAreaElement) return
      // While the shortcut sheet is up it owns Escape and "?"; swallow every
      // other player shortcut so nothing fires behind it.
      if (helpOpenRef.current) {
        if (event.key === 'Escape' || event.key === '?') {
          event.preventDefault()
          setHelpOpen(false)
        }
        return
      }
      if (event.key === '?') {
        event.preventDefault()
        setHelpOpen(true)
        return
      }
      switch (event.key.toLowerCase()) {
        case ' ':
          event.preventDefault()
          togglePlay()
          break
        case 'arrowleft':
          event.preventDefault()
          skip(-10)
          break
        case 'arrowright':
          event.preventDefault()
          skip(10)
          break
        case 'arrowup':
          event.preventDefault()
          changeVolume(0.05)
          break
        case 'arrowdown':
          event.preventDefault()
          changeVolume(-0.05)
          break
        case 'f':
          toggleFullscreen()
          break
        case 'm':
          if (videoRef.current) videoRef.current.muted = !videoRef.current.muted
          break
        case 'c':
          cycleSubtitles()
          break
        case 'escape':
          // In fullscreen the browser handles Escape (exits fullscreen); only
          // leave the player when we're not fullscreen.
          if (!document.fullscreenElement && !(document as FullscreenDocument).webkitFullscreenElement) {
            goBack()
          }
          break
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [goBack, changeVolume, cycleSubtitles, skip, toggleFullscreen, togglePlay])

  const onLoadedMetadata = () => {
    const video = videoRef.current
    if (!video) return
    setDuration(Number.isFinite(video.duration) ? video.duration : 0)
    const seekTo = pendingSeek.current
    pendingSeek.current = null
    const progress = item.data?.progress
    if (seekTo != null) {
      video.currentTime = Math.max(0, Math.min(video.duration || seekTo, seekTo))
    } else if (!resumed.current && resumeOverride != null) {
      // 3.2: an explicit `t` search param (Play = t=0) overrides stored progress.
      video.currentTime = Math.max(0, Math.min(video.duration || resumeOverride, resumeOverride))
    } else if (!resumed.current && progress && !progress.completed && progress.position_s > 0) {
      const maxResume = video.duration * 0.95
      if (progress.position_s < maxResume) video.currentTime = progress.position_s
    }
    resumed.current = true
    void video.play()
  }

  const onPointerDown = (event: ReactPointerEvent<HTMLDivElement>) => {
    const target = event.target
    // 1.1: lucide glyphs have SVGElement targets — Element (not HTMLElement)
    // still has .closest, so control taps are correctly ignored here.
    if (target instanceof Element && target.closest('[data-player-controls]')) return

    if (pointerRoute(event.pointerType) === 'mouse') {
      // 1.3: mouse — single click play/pause, double-click fullscreen. A ~250ms
      // timer distinguishes the two so a double-click doesn't pause-then-play.
      if (mouseClickDecision(singleClickTimer.current != null) === 'double') {
        if (singleClickTimer.current != null) {
          window.clearTimeout(singleClickTimer.current)
          singleClickTimer.current = null
        }
        toggleFullscreen()
        return
      }
      singleClickTimer.current = window.setTimeout(() => {
        singleClickTimer.current = null
        togglePlay()
      }, 250)
      registerActivity()
      return
    }

    // Touch / pen: single tap toggles controls, double-tap seeks (1.4 badge).
    const now = Date.now()
    const doubleTap = isDoubleTap(now, lastTap.current)
    lastTap.current = now
    const rect = event.currentTarget.getBoundingClientRect()
    const direction = seekDirection(event.clientX, rect.left, rect.width)
    const action = touchTapAction({ isDoubleTap: doubleTap, paused, direction })
    switch (action.type) {
      case 'seek':
        registerActivity()
        skip(action.direction * 10)
        flashSeek(action.direction)
        break
      case 'show-controls':
        registerActivity()
        break
      case 'toggle-controls':
        if (controlsVisible) setControlsVisible(false)
        else registerActivity()
        break
    }
  }

  const loading = item.isPending || session.isPending
  const error = item.isError || session.isError || videoError

  return (
    <main
      ref={containerRef}
      data-theme="dark"
      className="fixed inset-0 bg-canvas text-primary"
      // 1.6: manipulation stops Safari's double-tap-to-zoom fighting our seek.
      // Hide the cursor along with the chrome when controls auto-hide.
      style={{ touchAction: 'manipulation', cursor: controlsVisible || paused ? undefined : 'none' }}
      onMouseMove={registerActivity}
      onPointerDown={onPointerDown}
    >
      <video
        ref={videoRef}
        playsInline
        className="h-full w-full bg-black object-contain"
        onLoadedMetadata={onLoadedMetadata}
        onTimeUpdate={(e) => {
          const time = e.currentTarget.currentTime
          // Advancing time means playback is genuinely progressing, so any
          // pending/visible buffer spinner is spurious — clear it. A real
          // stall freezes `timeupdate`, so `onWaiting` still surfaces the
          // spinner and it clears again the moment playback resumes. This is
          // the reliable "we're playing" signal; `canplay`/`playing` don't
          // re-fire mid-playback.
          if (time !== currentTime) clearBuffering()
          setCurrentTime(time)
          const duration = e.currentTarget.duration
          if (Number.isFinite(duration) && duration > 0) {
            lastProgress.current = { position_s: time, duration_s: duration }
          }
        }}
        onDurationChange={(e) => setDuration(Number.isFinite(e.currentTarget.duration) ? e.currentTarget.duration : 0)}
        onProgress={(e) => readBuffered(e.currentTarget)}
        onWaiting={showBuffering}
        onPlaying={clearBuffering}
        onCanPlay={clearBuffering}
        onEmptied={() => {
          setBuffered([])
          clearBuffering()
        }}
        onPlay={() => setPaused(false)}
        onPause={() => {
          setPaused(true)
          setControlsVisible(true)
          // Paused videos stop actively buffering, so `waiting`/`stalled` may
          // never resolve to `canplay`/`playing` again until playback resumes
          // — clear here so the spinner doesn't get stuck through a pause.
          clearBuffering()
          persistProgress(false)
        }}
        onVolumeChange={(e) => {
          setMuted(e.currentTarget.muted)
          setVolume(e.currentTarget.volume)
        }}
        onRateChange={(e) => setPlaybackRate(e.currentTarget.playbackRate)}
        onError={() => setVideoError(true)}
      >
        {session.data?.subtitles.map((subtitle) => (
          <track
            key={subtitle.stream_index}
            data-stream-index={subtitle.stream_index}
            kind="subtitles"
            src={subtitle.url}
            srcLang={subtitle.lang ?? 'und'}
            label={subtitleLabel(subtitle.stream_index, subtitle.lang)}
          />
        ))}
      </video>

      <div
        className={[
          'pointer-events-none absolute inset-0 flex items-center justify-center bg-canvas/70 transition-opacity duration-[var(--duration-base)]',
          loading || error ? 'opacity-100' : 'opacity-0',
        ].join(' ')}
      >
        {loading && <Loader2 aria-hidden className="size-9 animate-spin text-secondary" strokeWidth={1.75} />}
        {error && (
          <div className="pointer-events-auto flex flex-col items-center gap-4">
            <p className="text-lg font-semibold">Playback failed</p>
            <Button
              variant="primary"
              onClick={() => {
                setVideoError(false)
                void session.refetch()
              }}
            >
              <RotateCcw aria-hidden className="size-4" strokeWidth={1.75} />
              Retry
            </Button>
          </div>
        )}
      </div>

      {buffering && !loading && !error && !paused && (
        <div className="pointer-events-none absolute inset-0 flex items-center justify-center" role="status">
          <Loader2 aria-hidden className="size-9 animate-spin text-secondary" strokeWidth={1.75} />
          <span className="sr-only">Buffering</span>
        </div>
      )}

      {seekIndicator && (
        <div
          key={seekIndicator.nonce}
          aria-hidden
          className={[
            'pointer-events-none absolute inset-y-0 flex w-1/2 items-center justify-center',
            seekIndicator.direction < 0 ? 'left-0' : 'right-0',
          ].join(' ')}
        >
          <span className="flex flex-col items-center gap-1 rounded-full bg-raised/90 px-5 py-4 text-primary shadow-overlay">
            {seekIndicator.direction < 0 ? (
              <RotateCcw aria-hidden className="size-6" strokeWidth={1.75} />
            ) : (
              <RotateCw aria-hidden className="size-6" strokeWidth={1.75} />
            )}
            <span className="tabular text-sm font-semibold">{seekIndicator.seconds}s</span>
          </span>
        </div>
      )}

      <div
        data-player-controls
        onPointerEnter={() => {
          hoveringControls.current = true
        }}
        onPointerLeave={() => {
          hoveringControls.current = false
        }}
        className={[
          'absolute inset-x-0 top-0 flex items-center justify-between p-4 transition-opacity duration-[var(--duration-base)]',
          controlsVisible || paused ? 'opacity-100' : 'pointer-events-none opacity-0',
        ].join(' ')}
      >
        <button
          type="button"
          onClick={goBack}
          className="inline-flex cursor-pointer items-center gap-2 rounded-sm text-sm text-primary"
        >
          <ArrowLeft aria-hidden className="size-4" strokeWidth={1.75} />
          {item.data?.title ?? 'Details'}
        </button>
      </div>

      <div
        data-player-controls
        onPointerEnter={() => {
          hoveringControls.current = true
        }}
        onPointerLeave={() => {
          hoveringControls.current = false
        }}
        className={[
          'absolute inset-x-0 bottom-0 bg-gradient-to-t from-black/80 to-transparent px-4 pt-20 pb-5 transition-opacity duration-[var(--duration-base)]',
          controlsVisible || paused ? 'opacity-100' : 'pointer-events-none opacity-0',
        ].join(' ')}
      >
        {/* Custom seek bar per DESIGN-SYSTEM player chrome: 4px base track,
            buffered ranges lighter behind an amber played range, thumb on
            hover/drag. The native range input stays on top (transparent track,
            styled thumb) so keyboard/ARIA slider semantics are unchanged. */}
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
              setCurrentTime(video.currentTime)
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
        <div className="flex flex-wrap items-center gap-2">
          <IconButton aria-label={paused ? 'Play' : 'Pause'} onClick={togglePlay}>
            {paused ? (
              <Play aria-hidden className="size-5" strokeWidth={1.75} />
            ) : (
              <Pause aria-hidden className="size-5" strokeWidth={1.75} />
            )}
          </IconButton>
          {/* Volume (mute + slider) sits between play and the timer. Hidden on
              coarse pointers: iOS ignores programmatic video.volume and hardware
              buttons set the level on touch. */}
          {!coarsePointer && (
            <>
              <IconButton
                aria-label={muted ? 'Unmute' : 'Mute'}
                onClick={() => {
                  if (videoRef.current) videoRef.current.muted = !videoRef.current.muted
                }}
              >
                {muted || volume === 0 ? (
                  <VolumeX aria-hidden className="size-5" strokeWidth={1.75} />
                ) : (
                  <Volume2 aria-hidden className="size-5" strokeWidth={1.75} />
                )}
              </IconButton>
              <input
                type="range"
                min={0}
                max={1}
                step={0.01}
                value={muted ? 0 : volume}
                onChange={(e) => {
                  const video = videoRef.current
                  if (!video) return
                  video.volume = Number(e.currentTarget.value)
                  video.muted = video.volume === 0
                }}
                aria-label="Volume"
                className="accent-accent-fill h-6 w-24"
              />
            </>
          )}
          <span className="tabular min-w-[104px] text-sm text-secondary">
            {formatClock(currentTime)} / {formatClock(duration)}
          </span>
          <span className="grow" />
          {/* Caption + speed: borderless, right-aligned, shown on every device
              (seek is touch double-tap; there's no on-screen hints button). */}
          {subtitleOptions.length > 0 && (
            <Menu
              aria-label="Subtitles"
              onOpenChange={(open) => {
                menuOpen.current = open
                registerActivity()
              }}
              trigger={
                <>
                  <Captions aria-hidden className="size-4 text-secondary" strokeWidth={1.75} />
                  {/* Icon-only on touch to save width; the menu's aria-label
                      still names it. Desktop shows the active track + caret. */}
                  {!coarsePointer && (
                    <>
                      <span className="max-w-24 truncate">
                        {selectedSubtitle ? subtitleOptionLabel(selectedSubtitle) : 'Off'}
                      </span>
                      <ChevronDown aria-hidden className="size-4" strokeWidth={1.75} />
                    </>
                  )}
                </>
              }
              triggerClassName="text-primary hover:bg-accent-subtle inline-flex h-11 cursor-pointer items-center gap-2 rounded-md px-3 text-sm"
            >
              <MenuItem checked={selectedSubtitle == null} onSelect={() => selectSubtitle(null)}>
                Off
              </MenuItem>
              {subtitleOptions.map((stream) => (
                <MenuItem
                  key={stream.stream_index}
                  checked={selectedSubtitleIndex === stream.stream_index}
                  onSelect={() => selectSubtitle(stream.stream_index)}
                >
                  {subtitleOptionLabel(stream)}
                </MenuItem>
              ))}
            </Menu>
          )}
          <Menu
            aria-label="Playback speed"
            onOpenChange={(open) => {
              menuOpen.current = open
              registerActivity()
            }}
            trigger={
              <>
                {playbackRate}x
                <ChevronDown aria-hidden className="size-4" strokeWidth={1.75} />
              </>
            }
            triggerClassName="text-primary hover:bg-accent-subtle inline-flex h-11 cursor-pointer items-center gap-1 rounded-md px-3 text-sm"
          >
            {[0.75, 1, 1.25, 1.5, 2].map((rate) => (
              <MenuItem
                key={rate}
                checked={playbackRate === rate}
                onSelect={() => {
                  if (videoRef.current) videoRef.current.playbackRate = rate
                }}
              >
                {rate}x
              </MenuItem>
            ))}
          </Menu>
          {airplaySupported && (
            <IconButton aria-label="AirPlay" onClick={showAirPlay}>
              <Cast aria-hidden className="size-5" strokeWidth={1.75} />
            </IconButton>
          )}
          <IconButton aria-label={fullscreen ? 'Exit fullscreen' : 'Fullscreen'} onClick={toggleFullscreen}>
            {fullscreen ? (
              <Minimize aria-hidden className="size-5" strokeWidth={1.75} />
            ) : (
              <Maximize aria-hidden className="size-5" strokeWidth={1.75} />
            )}
          </IconButton>
        </div>
      </div>

      {helpOpen && <KeyboardHelpSheet onClose={() => setHelpOpen(false)} />}
    </main>
  )
}

const SHORTCUTS: Array<{ keys: string[]; label: string }> = [
  { keys: ['Space'], label: 'Play or pause' },
  { keys: ['←', '→'], label: 'Skip back or forward 10 seconds' },
  { keys: ['↑', '↓'], label: 'Volume up or down' },
  { keys: ['F'], label: 'Toggle fullscreen' },
  { keys: ['M'], label: 'Toggle mute' },
  { keys: ['C'], label: 'Cycle subtitles' },
  { keys: ['?'], label: 'Show this help' },
  { keys: ['Esc'], label: 'Exit player' },
]

// A simple forced-dark overlay (the player subtree is always data-theme="dark",
// which a top-layer <dialog> would escape). Focus lands on the panel; Escape is
// handled by the player's window keydown, and a click on the scrim dismisses.
// Marked data-player-controls so the video tap handler ignores clicks inside.
function KeyboardHelpSheet({ onClose }: { onClose: () => void }) {
  const panelRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    panelRef.current?.focus()
  }, [])
  return (
    <div
      data-player-controls
      className="bg-overlay absolute inset-0 z-[var(--z-dialog)] flex items-center justify-center backdrop-blur-sm p-4"
      onClick={onClose}
    >
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-label="Keyboard shortcuts"
        tabIndex={-1}
        className="bg-raised border-line shadow-overlay w-full max-w-md rounded-lg border p-5 focus:outline-none"
        onClick={(event) => event.stopPropagation()}
      >
        <div className="mb-4 flex items-center justify-between">
          <h2 className="text-lg font-semibold">Keyboard shortcuts</h2>
          <IconButton aria-label="Close" onClick={onClose}>
            <X aria-hidden className="size-5" strokeWidth={1.75} />
          </IconButton>
        </div>
        <dl className="space-y-2.5">
          {SHORTCUTS.map((shortcut) => (
            <div key={shortcut.label} className="flex items-center justify-between gap-4">
              <dt className="text-sm text-secondary">{shortcut.label}</dt>
              <dd className="flex shrink-0 items-center gap-1">
                {shortcut.keys.map((key) => (
                  <kbd
                    key={key}
                    className="bg-inset border-line-strong text-primary tabular inline-flex min-w-7 items-center justify-center rounded-sm border px-2 py-0.5 text-xs font-medium"
                  >
                    {key}
                  </kbd>
                ))}
              </dd>
            </div>
          ))}
        </dl>
      </div>
    </div>
  )
}

function numeric(raw: string | null | undefined): number | null {
  if (!raw) return null
  const id = Number(raw)
  return Number.isInteger(id) && id > 0 ? id : null
}

function subtitleOptionLabel(stream: MediaStream): string {
  const base = subtitleLabel(stream.stream_index, stream.lang ?? stream.title)
  return isImageSubtitleCodec(stream.codec) ? `${base} (Burn in)` : base
}

function subtitleLabel(streamIndex: number, label?: string): string {
  return label ? label.toUpperCase() : `Subtitle ${streamIndex}`
}

function isTextSubtitleCodec(codec: string): boolean {
  return ['subrip', 'srt', 'ass', 'ssa', 'webvtt', 'mov_text', 'text'].includes(normalizeCodec(codec))
}

function isImageSubtitleCodec(codec: string): boolean {
  return ['hdmv_pgs_subtitle', 'pgs', 'dvd_subtitle', 'vobsub', 'xsub'].includes(normalizeCodec(codec))
}

function normalizeCodec(codec: string): string {
  const normalized = codec.trim().toLowerCase()
  if (normalized === 'srt') return 'subrip'
  if (normalized === 'pgs') return 'hdmv_pgs_subtitle'
  return normalized
}
