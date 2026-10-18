import type { Page, Route } from '@playwright/test'
import type {
  Collection,
  ItemSummary,
  LibraryEntry,
  ShowDetail,
  ShowSummary,
  UpNext,
} from '../../src/api/types.ts'

// Shared mocked-API builders for the shows wire (SPEC-API "Library grid",
// "Shows"). Builders return wire-exact objects; override what a case needs.

export type ItemEntry = { kind: 'item' } & ItemSummary
export type ShowEntry = { kind: 'show' } & ShowSummary

export function itemEntry(id: number, overrides: Partial<ItemSummary> = {}): ItemEntry {
  return {
    kind: 'item',
    id,
    type: 'movie',
    title: `Item ${id}`,
    year: 2024,
    duration_s: 6000,
    created_at: '2026-01-01 00:00:00',
    thumb_url: `/api/items/${id}/thumb?v=1`,
    available: true,
    collection_ids: [],
    show: null,
    season: null,
    episode: null,
    episode_end: null,
    needs_check: false,
    tmdb_id: null,
    art_url: null,
    ...overrides,
  }
}

export function upNext(itemID: number, overrides: Partial<UpNext> = {}): UpNext {
  return {
    item_id: itemID,
    season: 1,
    episode: 1,
    episode_end: null,
    title: `Episode ${itemID}`,
    available: true,
    thumb_url: `/api/items/${itemID}/thumb?v=1`,
    ...overrides,
  }
}

export function showEntry(id: number, overrides: Partial<ShowSummary> = {}): ShowEntry {
  return {
    kind: 'show',
    id,
    title: `Show ${id}`,
    year: 2024,
    thumb_url: `/api/shows/${id}/poster?v=1`,
    poster_url: `/api/shows/${id}/poster?v=1`,
    backdrop_url: `/api/shows/${id}/backdrop?v=1`,
    network: null,
    created_at: '2026-01-01 00:00:00',
    latest_added_at: '2026-01-02 00:00:00',
    season_count: 1,
    episode_count: 8,
    available: true,
    needs_check: false,
    has_progress: false,
    collection_ids: [],
    up_next: upNext(id * 100 + 1),
    ...overrides,
  }
}

export function showDetail(entry: ShowEntry, overrides: Partial<ShowDetail> = {}): ShowDetail {
  const { kind: _kind, ...summary } = entry
  void _kind
  return {
    ...summary,
    summary: null,
    tvmaze_id: null,
    season_offset: 0,
    title_locked: false,
    seasons: [],
    ...overrides,
  }
}

export interface LibraryMock {
  /** The grid, in server order; filtered here by `type` and `collection_id`. */
  entries: LibraryEntry[]
  /** What `in_progress=1` returns. */
  continueWatching: LibraryEntry[]
  collections: Collection[]
  shows: Record<number, ShowDetail>
}

export interface LibraryLog {
  libraryRequests: URLSearchParams[]
  collectionAdds: { collectionID: number; body: unknown }[]
  showTrash: number[]
  itemDeletes: number[]
  restores: number[]
}

/** Routes every /api request the library, show page and item pages make.
    Images are flat SVGs so cards render without fixtures on disk. Pass
    `extra` to claim more routes first (return true when handled). */
export async function mockLibraryApi(
  page: Page,
  mock: LibraryMock,
  extra?: (route: Route, path: string, url: URL) => Promise<boolean>,
): Promise<LibraryLog> {
  const log: LibraryLog = { libraryRequests: [], collectionAdds: [], showTrash: [], itemDeletes: [], restores: [] }

  // A glob like **/api/** would also catch Vite's /src/api/*.ts modules.
  await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname
    const method = request.method()

    if (extra && (await extra(route, path, url))) return

    if (method === 'GET' && /\/(thumb|poster|backdrop)$/.test(path)) {
      await route.fulfill({ status: 200, contentType: 'image/svg+xml', body: imageFor(path) })
      return
    }
    if (method === 'GET' && path === '/api/events') {
      await route.fulfill({ status: 204 })
      return
    }
    if (method === 'GET' && path === '/api/health') {
      await json(route, {
        version: 'test',
        uptime_s: 1,
        db_ok: true,
        roots: [{ id: 1, name: 'Media', online: true, free_bytes: 1e12 }],
        active_sessions: 0,
        queue_depth: 0,
      })
      return
    }
    if (method === 'GET' && path === '/api/collections') {
      await json(route, mock.collections)
      return
    }
    if (method === 'GET' && path === '/api/library') {
      const params = url.searchParams
      log.libraryRequests.push(params)
      if (params.get('in_progress') === '1') {
        await json(route, { total: mock.continueWatching.length, entries: mock.continueWatching })
        return
      }
      const type = params.get('type')
      const collectionID = Number(params.get('collection_id') ?? 0)
      const entries = mock.entries.filter(
        (entry) =>
          (type === 'show' ? entry.kind === 'show' : type === 'movie' ? entry.kind === 'item' && entry.type === 'movie' : true) &&
          (!collectionID || entry.collection_ids.includes(collectionID)),
      )
      await json(route, { total: entries.length, entries })
      return
    }
    const collectionAdd = path.match(/^\/api\/collections\/(\d+)\/items$/)
    if (method === 'POST' && collectionAdd) {
      log.collectionAdds.push({ collectionID: Number(collectionAdd[1]), body: request.postDataJSON() })
      await route.fulfill({ status: 204 })
      return
    }
    const showTrash = path.match(/^\/api\/shows\/(\d+)\/trash$/)
    if (method === 'POST' && showTrash) {
      const id = Number(showTrash[1])
      log.showTrash.push(id)
      await json(route, { item_ids: [id * 100 + 1, id * 100 + 2] })
      return
    }
    const show = path.match(/^\/api\/shows\/(\d+)$/)
    if (method === 'GET' && show && mock.shows[Number(show[1])]) {
      await json(route, mock.shows[Number(show[1])])
      return
    }
    const item = path.match(/^\/api\/items\/(\d+)$/)
    if (method === 'DELETE' && item) {
      log.itemDeletes.push(Number(item[1]))
      await route.fulfill({ status: 204 })
      return
    }
    const restore = path.match(/^\/api\/items\/(\d+)\/restore$/)
    if (method === 'POST' && restore) {
      log.restores.push(Number(restore[1]))
      await json(route, { id: Number(restore[1]) })
      return
    }

    await json(route, { error: { code: 'not_found', message: `no mock for ${method} ${path}` } }, 404)
  })

  return log
}

export async function json(route: Route, body: unknown, status = 200) {
  await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })
}

/** A flat, distinct colour per image URL, sized to its kind. */
function imageFor(path: string): string {
  const [w, h] = path.endsWith('/poster') ? [400, 600] : path.endsWith('/backdrop') ? [1920, 1080] : [640, 360]
  let hash = 0
  for (const ch of path) hash = (hash * 31 + ch.charCodeAt(0)) % 360
  return `<svg xmlns="http://www.w3.org/2000/svg" width="${w}" height="${h}" viewBox="0 0 ${w} ${h}"><defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="hsl(${hash} 45% 38%)"/><stop offset="1" stop-color="hsl(${(hash + 40) % 360} 35% 18%)"/></linearGradient></defs><rect width="${w}" height="${h}" fill="url(#g)"/></svg>`
}
