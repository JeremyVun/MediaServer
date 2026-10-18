import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, readFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import type { Page, Route } from '@playwright/test'
import type {
  ItemDetail,
  MissingEpisode,
  PlayMarkers,
  PlayResponse,
  ProgressUpdate,
  ShowDetail,
  ShowSeason,
} from '../../src/api/types.ts'
import { itemEntry, json, mockLibraryApi, showDetail, showEntry, upNext, type ItemEntry, type LibraryMock } from './library.ts'

export const FIXTURE_DURATION_S = 40

/** A 40 s VP9/Opus clip from lavfi, cached in the temp dir (never checked in).
    `frame` loops a still instead of the test pattern, for captures. */
export function playerFixture(frame?: string): string {
  const dir = join(tmpdir(), 'media-server-e2e')
  mkdirSync(dir, { recursive: true })
  const name = frame ? `still-tv-${frame.replace(/[^a-z0-9]+/gi, '-')}.webm` : 'testsrc-40s.webm'
  const out = join(dir, name)
  if (existsSync(out)) return out
  const video = frame
    ? ['-loop', '1', '-framerate', '24', '-i', frame]
    : ['-f', 'lavfi', '-i', `testsrc2=size=320x180:rate=24:duration=${FIXTURE_DURATION_S}`]
  execFileSync(
    'ffmpeg',
    [
      '-hide_banner', '-loglevel', 'error', '-y',
      ...video,
      '-f', 'lavfi', '-i', `sine=frequency=440:duration=${FIXTURE_DURATION_S}`,
      '-t', String(FIXTURE_DURATION_S),
      // JPEG stills are full range; Chromium's VP9 decoder rejects that flag.
      ...(frame ? ['-vf', 'scale=1280:-2,format=yuv420p', '-color_range', 'tv'] : []),
      '-c:v', 'libvpx-vp9', '-b:v', '300k', '-deadline', 'realtime', '-cpu-used', '8', '-g', '24', '-pix_fmt', 'yuv420p',
      '-c:a', 'libopus', '-b:a', '32k',
      `${out}.tmp.webm`,
    ],
    { timeout: 120_000 },
  )
  execFileSync('mv', [`${out}.tmp.webm`, out])
  return out
}

export function episode(id: number, season: number, number: number, title: string, overrides: Partial<ItemEntry> = {}): ItemEntry {
  return itemEntry(id, {
    type: 'episode',
    title,
    duration_s: FIXTURE_DURATION_S,
    show: { id: 1, title: 'Furious' },
    season,
    episode: number,
    ...overrides,
  })
}

export function missingEpisode(season: number, number: number, title: string, airdate = '2026-08-03'): MissingEpisode {
  return { kind: 'missing', season, number, title, airdate, missing: true }
}

export interface PlayerMock {
  /** Every playable item by id; episodes carry their show. */
  items: Record<number, ItemEntry>
  shows: Record<number, ShowDetail>
  markers?: Record<number, PlayMarkers>
  /** Library entries for the home page the tests start from. */
  entries?: LibraryMock['entries']
}

export interface PlayerLog {
  /** Every /api request in order, as `METHOD /path`. */
  api: { line: string; at: number }[]
  progress: { itemID: number; body: ProgressUpdate; at: number }[]
  plays: { itemID: number; at: number }[]
}

export async function mockPlayerApi(page: Page, mock: PlayerMock, file = playerFixture()): Promise<PlayerLog> {
  const log: PlayerLog = { api: [], progress: [], plays: [] }
  const media = readFileSync(file)
  page.on('request', (request) => {
    const url = new URL(request.url())
    if (url.pathname.startsWith('/api/')) log.api.push({ line: `${request.method()} ${url.pathname}`, at: Date.now() })
  })
  const library: LibraryMock = {
    entries: mock.entries ?? [],
    continueWatching: [],
    collections: [],
    shows: mock.shows,
  }
  await mockLibraryApi(page, library, async (route, path) => {
    const method = route.request().method()
    if (method === 'GET' && path.startsWith('/api/files/') && path.endsWith('/stream')) {
      await serveRange(route, media)
      return true
    }
    const play = path.match(/^\/api\/items\/(\d+)\/play$/)
    if (method === 'POST' && play) {
      const itemID = Number(play[1])
      log.plays.push({ itemID, at: Date.now() })
      const body: PlayResponse = {
        mode: 'direct',
        reason: null,
        url: `/api/files/${itemID}/stream`,
        subtitles: [],
        quality: 'original',
        qualities: [],
        audio_only: false,
        markers: mock.markers?.[itemID] ?? {},
      }
      await json(route, body)
      return true
    }
    const progress = path.match(/^\/api\/items\/(\d+)\/progress$/)
    if (method === 'PUT' && progress) {
      const body = route.request().postDataJSON() as ProgressUpdate
      log.progress.push({ itemID: Number(progress[1]), body, at: Date.now() })
      await json(route, { position_s: body.position_s, completed: body.completed ?? false })
      return true
    }
    const item = path.match(/^\/api\/items\/(\d+)$/)
    if (method === 'GET' && item && mock.items[Number(item[1])]) {
      await json(route, itemDetail(mock.items[Number(item[1])]))
      return true
    }
    return false
  })
  return log
}

function itemDetail(entry: ItemEntry): ItemDetail {
  const { kind: _kind, available: _available, duration_s: _duration, ...rest } = entry
  void _kind
  void _available
  void _duration
  return {
    ...rest,
    summary: null,
    updated_at: entry.created_at,
    deleted_at: null,
    files: [
      {
        id: entry.id,
        root_id: 1,
        rel_path: `${entry.title}.webm`,
        size: 1,
        container: 'webm',
        duration_s: FIXTURE_DURATION_S,
        width: 320,
        height: 180,
        bitrate: null,
        status: 'online',
        streams: [],
      },
    ],
  }
}

const RANGE_CHUNK = 512 * 1024

async function serveRange(route: Route, media: Buffer) {
  const range = route.request().headers()['range']
  const match = range?.match(/bytes=(\d*)-(\d*)/)
  if (!match) {
    await route.fulfill({ status: 200, body: media, headers: { 'Content-Type': 'video/webm', 'Accept-Ranges': 'bytes' } })
    return
  }
  const start = match[1] ? Number(match[1]) : 0
  const end = Math.min(match[2] ? Number(match[2]) : Infinity, start + RANGE_CHUNK - 1, media.length - 1)
  await route.fulfill({
    status: 206,
    body: media.subarray(start, end + 1),
    headers: {
      'Content-Type': 'video/webm',
      'Accept-Ranges': 'bytes',
      'Content-Range': `bytes ${start}-${end}/${media.length}`,
    },
  })
}

/** Furious: S1 E1–E3, E4 missing, E5–E6; S2 E1–E2; one special. Plus a
    one-season show and a movie. */
export function furiousMock(): PlayerMock {
  const s1 = [
    episode(101, 1, 1, 'Pilot', { progress: { position_s: FIXTURE_DURATION_S, completed: true } }),
    episode(102, 1, 2, 'Second Gear', { progress: { position_s: 12, completed: false } }),
    episode(103, 1, 3, 'Burnout'),
    missingEpisode(1, 4, 'Gone'),
    episode(105, 1, 5, 'Pick a Sticker'),
    episode(106, 1, 6, 'Redline'),
    episode(107, 1, 7, 'Offline Night', { available: false }),
  ]
  const s2 = [episode(201, 2, 1, 'Return'), episode(202, 2, 2, 'Pit Stop')]
  const specials = [episode(1, 0, 1, 'Behind the scenes')]
  const seasons: ShowSeason[] = [
    { season: 1, episodes: s1 },
    { season: 2, episodes: s2 },
    { season: 3, episodes: [missingEpisode(3, 1, 'Later'), missingEpisode(3, 2, 'Much later')] },
    { season: 0, episodes: specials },
  ]
  const furious = showDetail(
    showEntry(1, { title: 'Furious', has_progress: true, season_count: 3, up_next: upNext(103, { season: 1, episode: 3, title: 'Burnout' }) }),
    { seasons },
  )
  const solo = showDetail(showEntry(2, { title: 'Solo', up_next: upNext(301, { title: 'Only' }) }), {
    seasons: [{ season: 1, episodes: [episode(301, 1, 1, 'Only', { show: { id: 2, title: 'Solo' } }), episode(302, 1, 2, 'Lonely', { show: { id: 2, title: 'Solo' } })] }],
  })
  const items: Record<number, ItemEntry> = {}
  for (const season of [...seasons, ...solo.seasons]) {
    for (const entry of season.episodes) if (entry.kind === 'item') items[entry.id] = entry
  }
  items[900] = itemEntry(900, { title: 'Dune', duration_s: FIXTURE_DURATION_S })
  return {
    items,
    shows: { 1: furious, 2: solo },
    markers: {
      103: { intro: { start_s: 2, end_s: 8 }, credits_start_s: 30 },
      900: { intro: { start_s: 2, end_s: 8 }, credits_start_s: 30 },
    },
    entries: [showEntry(1, { title: 'Furious', has_progress: true, up_next: upNext(103, { season: 1, episode: 3, title: 'Burnout' }) })],
  }
}

/** Records when the first `playing` event fires (media events don't bubble,
    so listen in the capture phase). */
export async function trackFirstPlaying(page: Page): Promise<() => number | null> {
  let at: number | null = null
  await page.exposeFunction('__firstPlaying', () => {
    at ??= Date.now()
  })
  await page.addInitScript(() => {
    document.addEventListener('playing', () => (window as unknown as { __firstPlaying: () => void }).__firstPlaying(), {
      capture: true,
    })
  })
  return () => at
}
