import {
  Suspense,
  lazy,
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
} from 'react'
import type Hls from 'hls.js'
import { useLocation, useNavigate, useParams, useSearchParams } from 'react-router'
import {
  ArrowLeft,
  ArrowRight,
  AudioLines,
  Cast,
  Captions,
  ChevronDown,
  ListVideo,
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
  usePlayerShow,
  useSaveProgress,
} from '../api/queries.ts'
import type { MediaStream, ProgressUpdate, Quality, QualityRung } from '../api/types.ts'
import { parseResumeOverride } from '../lib/searchParams.ts'
import { Button, IconButton, Menu, MenuItem } from '../ui/index.ts'
import { AudioOnlyBackdrop } from './AudioOnlyBackdrop.tsx'
import { EPISODES_TRIGGER } from './episodesTrigger.ts'
import { mediaSessionMetadata } from './mediaSession.ts'
import { qualityAfterItemChange, readStoredQuality, resolveQualityLabel, writeStoredQuality } from './quality.ts'
import { Clock, SeekBar } from './SeekBar.tsx'
import { usePlaybackSession } from './usePlaybackSession.ts'
import {
  cardMode,
  inIntro,
  nextEpisode,
  pastCardStart,
  progressCompleted,
  wantsNextEpisode,
} from './nextEpisode.ts'
import {
  clampSkip,
  isDoubleTap,
  mouseClickDecision,
  playerTitles,
  pointerRoute,
  seekDirection,
  touchTapAction,
} from './playerLogic.ts'

const EpisodesPopover = lazy(() => import('./EpisodesPopover.tsx'))
const NextEpisodeCard = lazy(() => import('./NextEpisodeCard.tsx'))

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

// Tagged with the item whose source produced it, so a write never lands on
// the item the player has since switched to.
type PositionSample = { itemID: number; position_s: number; duration_s: number; creditsStartS?: number }

// Choices that belong to one item: stream indexes are per file, and a
// dismissed Next card or the halfway lookup must not carry to the next episode.
type ItemChoices = { itemID: number; subtitle: number | null; audio: number | null; wantsNext: boolean; dismissed: boolean }
const freshChoices = (itemID: number): ItemChoices => ({ itemID, subtitle: null, audio: null, wantsNext: false, dismissed: false })

const NO_QUALITIES: QualityRung[] = []

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
  // Set while native HLS is on trial: returns true if it took over the error by
  // switching to hls.js.
  const nativeHlsFallback = useRef<(() => boolean) | null>(null)
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
  // Reset in the render that sees a new id, so the new item's first /play
  // never carries the old file's stream picks.
  const [storedChoices, setChoices] = useState<ItemChoices>(() => freshChoices(itemID))
  const choices = storedChoices.itemID === itemID ? storedChoices : freshChoices(itemID)
  if (storedChoices.itemID !== itemID) setChoices(choices)
  const updateChoices = useCallback(
    (change: Partial<Omit<ItemChoices, 'itemID'>>) =>
      setChoices((prev) => (prev.itemID === itemID ? { ...prev, ...change } : prev)),
    [itemID],
  )
  const selectedSubtitleIndex = choices.subtitle
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
  const selectedAudioIndex = choices.audio
  const audioOptions = useMemo(
    () => activeFile?.streams.filter((stream) => stream.kind === 'audio') ?? [],
    [activeFile],
  )
  const defaultAudio = useMemo(
    () => audioOptions.find((stream) => stream.is_default) ?? audioOptions[0] ?? null,
    [audioOptions],
  )
  const selectedAudio = useMemo(
    () => audioOptions.find((stream) => stream.stream_index === selectedAudioIndex) ?? defaultAudio,
    [audioOptions, defaultAudio, selectedAudioIndex],
  )
  // Browsers can't switch tracks on a plain <video src>, so a non-default pick
  // is remapped server-side via an HLS session; the default track is omitted
  // from the request and keeps direct play.
  const sessionAudioIndex =
    selectedAudio && selectedAudio.stream_index !== defaultAudio?.stream_index ? selectedAudio.stream_index : null
  // The *requested* quality: the remembered preference until the viewer picks
  // another. The server may resolve it down for a smaller file, but that
  // resolved value is never stored, so a remembered 1080p still applies to the
  // next 1080p file.
  const [requested, setRequested] = useState<{ itemID: number; quality: Quality }>(() => ({
    itemID,
    quality: readStoredQuality(),
  }))
  // Audio only is never stored, so another item has to fall back to the stored
  // preference in the very render that sees the new id — the /play request
  // below is keyed on the value.
  const requestedQuality =
    requested.itemID === itemID
      ? requested.quality
      : qualityAfterItemChange(requested.quality, readStoredQuality())
  if (requested.itemID !== itemID) setRequested({ itemID, quality: requestedQuality })
  const session = usePlaybackSession(itemID, fileID, burnInSubtitleIndex, sessionAudioIndex, requestedQuality)
  const { mutate: saveProgressMutate, mutateAsync: saveProgressAsync } = useSaveProgress(itemID)
  const markers = session.data?.markers
  const episodeShow = item.data?.type === 'episode' ? item.data.show : null
  const showID = episodeShow?.id ?? null
  const isEpisode = showID != null

  const [paused, setPaused] = useState(true)
  const [muted, setMuted] = useState(false)
  const [volume, setVolume] = useState(1)
  const [duration, setDuration] = useState(0)
  const [playbackRate, setPlaybackRate] = useState(1)
  // Height of the frames playing now, from the video element's `resize` event —
  // how Auto reports the size an HLS variant switch landed on.
  const [playingHeight, setPlayingHeight] = useState(0)
  // A quality or audio change re-POSTs /play and the session query has no
  // data until it lands; holding the last list keeps the menu mounted.
  const [held, setHeld] = useState<{ itemID: number; qualities: QualityRung[]; audioOnly: boolean }>({
    itemID,
    qualities: NO_QUALITIES,
    audioOnly: false,
  })
  if (
    session.data &&
    (held.itemID !== itemID ||
      held.qualities !== session.data.qualities ||
      held.audioOnly !== session.data.audio_only)
  ) {
    setHeld({ itemID, qualities: session.data.qualities, audioOnly: session.data.audio_only })
  }
  const heldQualities = held.itemID === itemID ? held.qualities : NO_QUALITIES
  const heldAudioOnly = held.itemID === itemID && held.audioOnly
  const [controlsVisible, setControlsVisible] = useState(true)
  const [fullscreen, setFullscreen] = useState(false)
  const [videoError, setVideoError] = useState(false)
  const [seekIndicator, setSeekIndicator] = useState<SeekIndicator | null>(null)
  // Spinner shown on `waiting` (playback stalled for lack of buffered frames),
  // debounced so routine seeks don't flash it. Deliberately NOT on `stalled`:
  // that's a network-idle event that fires during healthy playback once the
  // buffer fills, so it wrongly sticks the spinner on until the next pause/play.
  const [buffering, setBuffering] = useState(false)
  const [helpOpen, setHelpOpen] = useState(false)
  const [airplaySupported] = useState(
    () => typeof HTMLVideoElement !== 'undefined' && 'webkitShowPlaybackTargetPicker' in HTMLVideoElement.prototype,
  )
  // iOS ignores programmatic video.volume, so hide the slider on coarse pointers.
  const [coarsePointer] = useState(
    () => typeof window !== 'undefined' && window.matchMedia?.('(pointer: coarse)').matches === true,
  )
  // Position-derived flags, set from `timeupdate` only when they flip so the
  // player doesn't re-render at 4 Hz.
  const [introActive, setIntroActive] = useState(false)
  const [pastStart, setPastStart] = useState(false)
  const [ended, setEnded] = useState(false)
  const [episodesMounted, setEpisodesMounted] = useState(false)
  const [episodesOpen, setEpisodesOpen] = useState(false)
  // Bottom offset that keeps Skip intro and the Next card clear of the
  // control bar while it shows.
  const [lift, setLift] = useState<number | null>(null)
  const episodesID = useId()

  // Last observed playback position, refreshed on every timeupdate. The
  // unmount save (back navigation) runs after React has already detached
  // videoRef, so it must read from here — reading the <video> element at that
  // point returns nothing and the final position would be silently dropped.
  const lastProgress = useRef<PositionSample | null>(null)
  // The item and credits marker of the source attached to <video>; null while
  // none is. Set by the session effect, so events from a source that is being
  // torn down are never credited to the item just switched to.
  const source = useRef<{ itemID: number; creditsStartS?: number } | null>(null)
  // The item the viewer advanced from via the Next card: its trailing saves
  // keep `completed`, so the unmount flush can't undo the advance.
  const advancedItem = useRef<number | null>(null)
  const advancing = useRef(false)

  const progressPayload = useCallback(() => {
    const video = videoRef.current
    const current = source.current
    if (current && video && Number.isFinite(video.duration) && video.duration > 0) {
      lastProgress.current = {
        itemID: current.itemID,
        position_s: video.currentTime,
        duration_s: video.duration,
        creditsStartS: current.creditsStartS,
      }
    }
    return lastProgress.current
  }, [])

  const persistProgress = useCallback(
    (beacon = false) => {
      const sample = progressPayload()
      if (!sample || sample.itemID !== itemID) return
      const payload: ProgressUpdate = { position_s: sample.position_s, duration_s: sample.duration_s }
      const advanced = advancedItem.current === itemID
      if (progressCompleted({ positionS: sample.position_s, creditsStartS: sample.creditsStartS, advanced })) {
        payload.completed = true
      }
      if (beacon) {
        saveProgressBeacon(itemID, payload)
      } else {
        saveProgressMutate(payload)
      }
    },
    [itemID, progressPayload, saveProgressMutate],
  )

  // Navigating between items reuses this component; the old item's final
  // position has already been flushed by the effect cleanups, which run
  // before this, so drop every per-item ref before the new item starts.
  useEffect(() => {
    lastProgress.current = null
    pendingSeek.current = null
    advancedItem.current = null
    advancing.current = false
  }, [itemID])

  const titles = item.data ? playerTitles(item.data) : null
  const mediaTitle = titles?.mediaTitle
  const mediaArtist = titles?.mediaArtist
  const itemThumbURL = item.data?.thumb_url
  // Lock-screen and Control Centre metadata. No action handlers: the browser
  // drives play/pause through the element on its own.
  useEffect(() => {
    if (!('mediaSession' in navigator) || mediaTitle == null) return
    navigator.mediaSession.metadata = new MediaMetadata(
      mediaSessionMetadata(mediaTitle, itemThumbURL ?? '', window.location.origin, mediaArtist),
    )
    return () => {
      navigator.mediaSession.metadata = null
    }
  }, [mediaTitle, mediaArtist, itemThumbURL])

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

  // Last position seen by `timeupdate` or a seek; advancing past it proves
  // playback is progressing, which clears a pending buffer spinner.
  const lastTime = useRef(0)

  // Audio, subtitle and quality changes re-POST /play; the new stream resumes
  // from here via onLoadedMetadata.
  const rememberPosition = useCallback(() => {
    const video = videoRef.current
    if (video && Number.isFinite(video.currentTime)) pendingSeek.current = video.currentTime
  }, [])

  const onMenuOpenChange = useCallback(
    (open: boolean) => {
      menuOpen.current = open
      registerActivity()
    },
    [registerActivity],
  )

  const onEpisodesOpenChange = useCallback(
    (open: boolean) => {
      setEpisodesOpen(open)
      onMenuOpenChange(open)
    },
    [onMenuOpenChange],
  )

  const switchTo = useCallback(
    (nextItemID: number) => navigate(`/watch/${nextItemID}`, { replace: true, state: location.state }),
    [location.state, navigate],
  )

  const showData = usePlayerShow(showID, choices.wantsNext).data
  const upNext = showData && isEpisode ? nextEpisode(showData, itemID) : null
  const card = upNext ? cardMode({ pastStart, ended, dismissed: choices.dismissed }) : 'hidden'

  // The completed write lands before the switch, so the show's up-next has
  // moved on by the time anything refetches it.
  const advance = useCallback(async () => {
    if (!upNext || advancing.current) return
    advancing.current = true
    advancedItem.current = itemID
    const sample = progressPayload()
    const fallback = { position_s: duration, duration_s: duration }
    const position = sample?.itemID === itemID ? sample : fallback
    try {
      await saveProgressAsync({ position_s: position.position_s, duration_s: position.duration_s, completed: true })
    } catch {
      // The switch still happens; the server also counts the credits as watched.
    }
    switchTo(upNext.id)
  }, [duration, itemID, progressPayload, saveProgressAsync, switchTo, upNext])

  const dismissCard = useCallback(() => updateChoices({ dismissed: true }), [updateChoices])

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
      if (currentBurnIn || nextBurnIn) rememberPosition()
      updateChoices({ subtitle: streamIndex })
    },
    [rememberPosition, selectedSubtitle, subtitleOptions, updateChoices],
  )

  const selectAudio = useCallback(
    (streamIndex: number) => {
      const next = audioOptions.find((stream) => stream.stream_index === streamIndex) ?? defaultAudio
      const nextSessionIndex =
        next && next.stream_index !== defaultAudio?.stream_index ? next.stream_index : null
      if (nextSessionIndex !== sessionAudioIndex) rememberPosition()
      updateChoices({ audio: streamIndex })
    },
    [audioOptions, defaultAudio, rememberPosition, sessionAudioIndex, updateChoices],
  )

  const selectQuality = useCallback(
    (quality: Quality) => {
      // Audio only is a per-listen pick, never the stored preference.
      if (quality !== 'audio') writeStoredQuality(quality)
      if (quality !== requestedQuality) rememberPosition()
      setRequested({ itemID, quality })
    },
    [itemID, rememberPosition, requestedQuality],
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
    source.current = { itemID, creditsStartS: session.data.markers?.credits_start_s }

    const url = session.data.url
    const attachHlsJs = () => {
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
          hls.on(Hls.Events.MANIFEST_PARSED, () => {
            // hls.js orders levels by bitrate; start on the one the server listed
            // first, as Safari's native player does.
            hls.startLevel = hls.firstLevel
          })
          hls.loadSource(url)
          hls.attachMedia(video)
        })
        .catch(() => {
          if (!cancelled) setVideoError(true)
        })
    }

    if (session.data.mode === 'direct') {
      video.src = url
      video.load()
    } else if (session.capabilities.native_hls) {
      // Some Brave profiles answer "maybe" for HLS and then reject every
      // playlist with "no supported sources"; hls.js plays those fine.
      nativeHlsFallback.current = () => {
        nativeHlsFallback.current = null
        if (video.error?.code !== MediaError.MEDIA_ERR_SRC_NOT_SUPPORTED) return false
        video.removeAttribute('src')
        attachHlsJs()
        return true
      }
      video.src = url
      video.load()
    } else {
      attachHlsJs()
    }

    return () => {
      cancelled = true
      source.current = null
      nativeHlsFallback.current = null
      hlsRef.current?.destroy()
      hlsRef.current = null
      video.removeAttribute('src')
      video.load()
    }
    // session.dataUpdatedAt changes on every fetch, including a refetch that
    // resolves to a structurally-shared (reference-equal) `data` — needed so
    // the Retry button actually reloads the element for direct-play sessions.
  }, [itemID, session.capabilities.native_hls, session.data, session.dataUpdatedAt])

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

  const chromeShown = controlsVisible || paused
  useLayoutEffect(() => {
    if (!isEpisode) return
    const measure = () => {
      const seek = containerRef.current?.querySelector('[data-seek]')
      setLift(chromeShown && seek ? window.innerHeight - seek.getBoundingClientRect().top + 8 : null)
    }
    measure()
    window.addEventListener('resize', measure)
    return () => window.removeEventListener('resize', measure)
  }, [chromeShown, isEpisode])

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

  const qualities = session.data?.qualities ?? heldQualities
  const audioOnlyOffered = session.data?.audio_only ?? heldAudioOnly
  const resolvedQuality = session.data?.quality ?? requestedQuality
  const qualityLabel = resolveQualityLabel(resolvedQuality, qualities, playingHeight)
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
          if (time !== lastTime.current) clearBuffering()
          lastTime.current = time
          const duration = e.currentTarget.duration
          const current = source.current
          if (!current || current.itemID !== itemID || !Number.isFinite(duration) || duration <= 0) return
          lastProgress.current = { itemID, position_s: time, duration_s: duration, creditsStartS: current.creditsStartS }
          if (!isEpisode) return
          setIntroActive(inIntro(time, markers))
          const past = pastCardStart(time, duration, markers)
          setPastStart(past)
          if (!past && choices.dismissed) updateChoices({ dismissed: false })
          if (!choices.wantsNext && wantsNextEpisode(time, duration)) updateChoices({ wantsNext: true })
        }}
        onEnded={() => setEnded(true)}
        onDurationChange={(e) => setDuration(Number.isFinite(e.currentTarget.duration) ? e.currentTarget.duration : 0)}
        onSeeking={(e) => {
          lastTime.current = e.currentTarget.currentTime
          setEnded(false)
        }}
        onWaiting={showBuffering}
        onPlaying={clearBuffering}
        onCanPlay={clearBuffering}
        onResize={(e) => setPlayingHeight(e.currentTarget.videoHeight)}
        onEmptied={() => {
          setPlayingHeight(0)
          clearBuffering()
          setIntroActive(false)
          setPastStart(false)
          setEnded(false)
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
        onError={() => {
          if (nativeHlsFallback.current?.()) return
          setVideoError(true)
        }}
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

      {resolvedQuality === 'audio' && <AudioOnlyBackdrop thumbURL={itemThumbURL} />}

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
          'absolute inset-x-0 top-0 flex items-center justify-between gap-4 p-4 transition-opacity duration-[var(--duration-base)]',
          controlsVisible || paused ? 'opacity-100' : 'pointer-events-none opacity-0',
        ].join(' ')}
      >
        <button
          type="button"
          onClick={goBack}
          className="inline-flex min-w-0 cursor-pointer items-center gap-2 rounded-sm text-sm text-primary"
        >
          <ArrowLeft aria-hidden className="size-4 shrink-0" strokeWidth={1.75} />
          <span className="truncate">{titles?.heading ?? 'Details'}</span>
        </button>
        {isEpisode && (
          <button
            type="button"
            {...{ [EPISODES_TRIGGER]: 'top' }}
            popoverTarget={episodesID}
            aria-label="Episodes"
            aria-haspopup="dialog"
            aria-expanded={episodesOpen}
            onClick={() => setEpisodesMounted(true)}
            className={[
              '-my-3 -mr-2 inline-flex size-11 shrink-0 cursor-pointer items-center justify-center rounded-md text-primary sm:hidden',
              episodesOpen ? 'bg-accent-subtle' : '',
            ].join(' ')}
          >
            <ListVideo aria-hidden className="size-5" strokeWidth={1.75} />
          </button>
        )}
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
        <SeekBar videoRef={videoRef} duration={duration} coarsePointer={coarsePointer} />
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
          <Clock videoRef={videoRef} duration={duration} />
          <span className="grow" />
          {/* Audio, caption, speed and quality: borderless, right-aligned, shown on every
              device (seek is touch double-tap; there's no on-screen hints
              button). Audio only appears when there's a track to switch to. */}
          {audioOptions.length > 1 && (
            <PlayerMenu
              label="Audio track"
              gap="gap-2"
              onOpenChange={onMenuOpenChange}
              trigger={
                <>
                  <AudioLines aria-hidden className="size-4 text-secondary" strokeWidth={1.75} />
                  {!coarsePointer && (
                    <>
                      <span className="max-w-24 truncate">
                        {selectedAudio ? audioOptionLabel(selectedAudio) : 'Default'}
                      </span>
                      <ChevronDown aria-hidden className="size-4" strokeWidth={1.75} />
                    </>
                  )}
                </>
              }
            >
              {audioOptions.map((stream) => (
                <MenuItem
                  key={stream.stream_index}
                  checked={selectedAudio?.stream_index === stream.stream_index}
                  onSelect={() => selectAudio(stream.stream_index)}
                >
                  {audioOptionLabel(stream)}
                </MenuItem>
              ))}
            </PlayerMenu>
          )}
          {subtitleOptions.length > 0 && (
            <PlayerMenu
              label="Subtitles"
              gap="gap-2"
              onOpenChange={onMenuOpenChange}
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
            </PlayerMenu>
          )}
          <PlayerMenu
            label="Playback speed"
            gap="gap-1"
            onOpenChange={onMenuOpenChange}
            trigger={
              <>
                {playbackRate}x
                <ChevronDown aria-hidden className="size-4" strokeWidth={1.75} />
              </>
            }
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
          </PlayerMenu>
          {qualities.length > 0 && (
            <PlayerMenu
              label="Quality"
              gap="gap-1"
              onOpenChange={onMenuOpenChange}
              trigger={
                <>
                  {qualityLabel}
                  <ChevronDown aria-hidden className="size-4" strokeWidth={1.75} />
                </>
              }
            >
              <MenuItem checked={resolvedQuality === 'auto'} onSelect={() => selectQuality('auto')}>
                {resolvedQuality === 'auto' ? qualityLabel : 'Auto'}
              </MenuItem>
              <MenuItem checked={resolvedQuality === 'original'} onSelect={() => selectQuality('original')}>
                Original
              </MenuItem>
              {qualities.map((rung) => (
                <MenuItem
                  key={rung.id}
                  checked={resolvedQuality === rung.id}
                  onSelect={() => selectQuality(rung.id)}
                >
                  {rung.id}
                </MenuItem>
              ))}
              {audioOnlyOffered && (
                <MenuItem checked={resolvedQuality === 'audio'} onSelect={() => selectQuality('audio')}>
                  Audio only
                </MenuItem>
              )}
            </PlayerMenu>
          )}
          {airplaySupported && (
            <IconButton aria-label="AirPlay" onClick={showAirPlay}>
              <Cast aria-hidden className="size-5" strokeWidth={1.75} />
            </IconButton>
          )}
          {isEpisode && (
            <button
              type="button"
              {...{ [EPISODES_TRIGGER]: 'bar' }}
              popoverTarget={episodesID}
              aria-label="Episodes"
              aria-haspopup="dialog"
              aria-expanded={episodesOpen}
              onClick={() => setEpisodesMounted(true)}
              className={[
                'text-primary hidden h-11 cursor-pointer items-center gap-2 rounded-md px-3 text-sm sm:inline-flex',
                episodesOpen ? 'bg-accent-subtle' : 'hover:bg-accent-subtle',
              ].join(' ')}
            >
              <ListVideo aria-hidden className="size-4 text-secondary" strokeWidth={1.75} />
              Episodes
            </button>
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

      {isEpisode && introActive && markers?.intro && (
        <button
          type="button"
          data-player-controls
          onClick={() => {
            const video = videoRef.current
            if (video && markers.intro) video.currentTime = markers.intro.end_s
          }}
          style={lift != null ? { bottom: lift } : undefined}
          className="text-primary absolute bottom-6 left-1 inline-flex h-11 cursor-pointer items-center gap-2 rounded-md px-3 text-base font-medium transition-[bottom] duration-[var(--duration-base)] sm:bottom-10 sm:left-9"
        >
          Skip intro
          <ArrowRight aria-hidden className="size-4" strokeWidth={1.75} />
        </button>
      )}

      {upNext && card !== 'hidden' && (
        <Suspense fallback={null}>
          <NextEpisodeCard
            key={`${upNext.id}-${card}`}
            episode={upNext}
            countdown={card === 'countdown'}
            running={!paused || ended}
            lift={lift}
            onPlay={() => void advance()}
            onDismiss={dismissCard}
          />
        </Suspense>
      )}

      {showID != null && episodesMounted && (
        <Suspense fallback={null}>
          <EpisodesPopover
            id={episodesID}
            showID={showID}
            currentItemID={itemID}
            onPick={switchTo}
            onOpenChange={onEpisodesOpenChange}
          />
        </Suspense>
      )}

      {helpOpen && <KeyboardHelpSheet onClose={() => setHelpOpen(false)} />}
    </main>
  )
}

const MENU_TRIGGER_CLASS =
  'text-primary hover:bg-accent-subtle inline-flex h-11 cursor-pointer items-center rounded-md px-3 text-sm'

// Control-bar menus share their trigger chrome and keep the auto-hide timer
// informed while open. Icon+label triggers use gap-2, text-only ones gap-1.
function PlayerMenu({
  label,
  gap,
  onOpenChange,
  trigger,
  children,
}: {
  label: string
  gap: 'gap-1' | 'gap-2'
  onOpenChange: (open: boolean) => void
  trigger: ReactNode
  children: ReactNode
}) {
  return (
    <Menu
      aria-label={label}
      onOpenChange={onOpenChange}
      trigger={trigger}
      triggerClassName={`${MENU_TRIGGER_CLASS} ${gap}`}
    >
      {children}
    </Menu>
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

function audioOptionLabel(stream: MediaStream): string {
  const base = stream.title ?? stream.lang?.toUpperCase() ?? `Track ${stream.stream_index}`
  const layout = channelLayoutLabel(stream.channels)
  return layout ? `${base} · ${layout}` : base
}

function channelLayoutLabel(channels?: number): string | null {
  switch (channels) {
    case undefined:
      return null
    case 1:
      return 'Mono'
    case 2:
      return 'Stereo'
    case 6:
      return '5.1'
    case 8:
      return '7.1'
    default:
      return `${channels}ch`
  }
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
