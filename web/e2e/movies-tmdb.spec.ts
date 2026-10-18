import { expect, test, type Page } from '@playwright/test'
import type { ItemDetail, MovieMatchCandidate } from '../src/api/types.ts'
import { itemEntry, json, mockLibraryApi, type LibraryMock } from './support/library.ts'

// TMDB movie art, the movie splash page, the TMDB Change dialog and the
// attribution, against the mocked wire (SPEC-API "TMDB match (movies)").

const NOTICE = 'This product uses the TMDB API but is not endorsed or certified by TMDB.'
const SUMMARY =
  'The defense and the prosecution have rested and the jury is filing into the jury room to decide if a young Spanish-American is guilty or innocent of murdering his father. '.repeat(
    4,
  )
const CANDIDATES: MovieMatchCandidate[] = [
  { tmdb_id: 1246049, title: 'Dracula', year: 2025, poster_url: null },
  { tmdb_id: 6114, title: 'Bram Stoker’s Dracula', year: 1992, poster_url: null },
]

function movie(overrides: Partial<ItemDetail> = {}): ItemDetail {
  return {
    id: 700,
    type: 'movie',
    title: '12 Angry Men',
    year: 1957,
    summary: SUMMARY,
    created_at: '2026-01-01 00:00:00',
    updated_at: '2026-01-01 00:00:00',
    deleted_at: null,
    collection_ids: [],
    thumb_url: '/api/items/700/thumb?v=1',
    tmdb_id: 389,
    art_url: '/api/items/700/art?v=1',
    files: [
      {
        id: 1,
        root_id: 1,
        rel_path: 'Movies/12.Angry.Men.1957.mkv',
        size: 2e9,
        container: 'matroska',
        duration_s: 5820,
        width: 1920,
        height: 1080,
        bitrate: null,
        status: 'online',
        streams: [],
      },
    ],
    show: null,
    season: null,
    episode: null,
    episode_end: null,
    needs_check: false,
    ...overrides,
  }
}

const dracula = () =>
  movie({ id: 701, title: 'Dracula A Love Tale', year: 2025, summary: null, tmdb_id: null, art_url: null, needs_check: true })

interface Log {
  searches: URLSearchParams[]
  matches: unknown[]
  patches: unknown[]
}

/** Serves one item, settings with `configured`, and the TMDB endpoints. */
async function mockMovie(
  page: Page,
  initial: ItemDetail,
  {
    configured = true,
    entries = [],
    search = () => ({ status: 200, body: { results: CANDIDATES } }),
    match = (body, item) => ({ status: 200, body: { ...item, tmdb_id: body.tmdb_id, needs_check: false } }),
  }: {
    configured?: boolean
    entries?: LibraryMock['entries']
    search?: (params: URLSearchParams) => { status: number; body: unknown }
    match?: (body: { tmdb_id: number | null }, item: ItemDetail) => { status: number; body: unknown }
  } = {},
): Promise<Log> {
  let item = initial
  const log: Log = { searches: [], matches: [], patches: [] }
  const mock: LibraryMock = { entries, continueWatching: [], collections: [], shows: {} }
  await mockLibraryApi(page, mock, async (route, path, url) => {
    const method = route.request().method()
    if (path === '/api/settings') {
      await json(route, {
        hls_cache: { dir: '/Volumes/Media/.hls', max_bytes: 5e10, used_bytes: 1e9, available: true },
        semantic_classification: { enabled: false, available: true },
        tmdb: { configured },
      })
      return true
    }
    if (/^\/api\/items\/\d+\/art$/.test(path)) {
      const svg = '<svg xmlns="http://www.w3.org/2000/svg" width="1280" height="720"><rect width="1280" height="720" fill="rgb(90,90,90)"/></svg>'
      await route.fulfill({ status: 200, contentType: 'image/svg+xml', body: svg })
      return true
    }
    if (path === `/api/items/${item.id}/match-search`) {
      log.searches.push(url.searchParams)
      const { status, body } = search(url.searchParams)
      await json(route, body, status)
      return true
    }
    if (path === `/api/items/${item.id}/match` && method === 'POST') {
      const body = route.request().postDataJSON() as { tmdb_id: number | null }
      log.matches.push(body)
      const answer = match(body, item)
      if (answer.status === 200) item = answer.body as ItemDetail
      await json(route, answer.body, answer.status)
      return true
    }
    if (path === `/api/items/${item.id}` && method === 'PATCH') {
      const patch = route.request().postDataJSON() as Record<string, unknown>
      log.patches.push(patch)
      item = { ...item, ...patch, needs_check: false, updated_at: 'later' }
      await json(route, item)
      return true
    }
    if (path === `/api/items/${item.id}` && method === 'GET') {
      await json(route, item)
      return true
    }
    if (path === '/api/jobs') {
      await json(route, [])
      return true
    }
    if (path === '/api/roots') {
      await json(route, [])
      return true
    }
    if (path === '/api/items') {
      await json(route, { total: 0, items: [] })
      return true
    }
    return false
  })
  return log
}

test('a movie card shows its TMDB backdrop, else its frame thumbnail', async ({ page }) => {
  const entries = [
    itemEntry(10, { title: 'Matched', art_url: '/api/items/10/art?v=1', tmdb_id: 1 }),
    itemEntry(11, { title: 'Unmatched', needs_check: true }),
    itemEntry(12, { title: 'Broken art', art_url: '/api/items/12/broken?v=1', tmdb_id: 2 }),
  ]
  await mockMovie(page, movie(), { entries })
  await page.goto('/')

  const card = (title: string) => page.locator('[data-library-grid]').getByRole('link', { name: `Play ${title}` })
  await expect(card('Matched').locator('img')).toHaveAttribute('src', '/api/items/10/art?v=1')
  await expect(card('Unmatched').locator('img')).toHaveAttribute('src', '/api/items/11/thumb?v=1')
  // B15: an unsure match carries no card badge; the item page's Check strip handles it.
  await expect(card('Unmatched').getByText('Check')).toHaveCount(0)
  // Art that fails to load falls back to the frame.
  await expect(card('Broken art').locator('img')).toHaveAttribute('src', '/api/items/12/thumb?v=1')
})

test('invariant 30: a matched movie page uses the splash header and carries the TMDB notice', async ({ page }) => {
  await mockMovie(page, movie())
  await page.goto('/items/700')

  await expect(page.locator('[data-show-page][data-art] [data-splash] img')).toHaveAttribute('src', '/api/items/700/art?v=1')
  await expect(page.getByRole('heading', { level: 1, name: '12 Angry Men' })).toBeVisible()
  await expect(page.locator('[data-hero]').getByText('1957 · 1 hr 37 min')).toBeVisible()

  const summary = page.getByRole('button', { name: /The defense and the prosecution/ })
  await expect(summary).toHaveAttribute('aria-expanded', 'false')
  const clamped = await summary.evaluate((el) => el.getBoundingClientRect().height)
  await summary.click()
  await expect(summary).toHaveAttribute('aria-expanded', 'true')
  expect(await summary.evaluate((el) => el.getBoundingClientRect().height)).toBeGreaterThan(clamped)
  expect(clamped).toBeLessThan(4 * 28)

  await expect(page.getByText(NOTICE)).toBeVisible()
  await expect(page.getByRole('button', { name: 'Play', exact: true })).toBeEnabled()
  await expect(page.getByRole('heading', { name: 'Files' })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Collections' })).toBeVisible()
})

test('the splash page edits details from More', async ({ page }) => {
  const log = await mockMovie(page, movie())
  await page.goto('/items/700')
  await page.getByRole('button', { name: 'More' }).click()
  await page.getByRole('menuitem', { name: 'Edit details' }).click()
  await page.getByLabel('Title').fill('Twelve Angry Men')
  await page.getByRole('button', { name: 'Save' }).click()
  await expect.poll(() => log.patches).toEqual([{ title: 'Twelve Angry Men' }])
  await expect(page.getByRole('heading', { level: 1, name: 'Twelve Angry Men' })).toBeVisible()
})

test('a movie without art keeps the plain layout, with the notice when matched', async ({ page }) => {
  await mockMovie(page, movie({ art_url: null }))
  await page.goto('/items/700')
  await expect(page.getByRole('heading', { level: 1, name: '12 Angry Men' })).toBeVisible()
  await expect(page.locator('[data-splash]')).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Edit item' })).toBeVisible()
  await expect(page.getByText(NOTICE)).toBeVisible()
})

test('invariant 30: an unmatched movie keeps its frame and the Check strip, and Change picks a TMDB candidate', async ({ page }) => {
  const log = await mockMovie(page, dracula())
  await page.goto('/items/701')

  await expect(page.locator('[data-splash]')).toHaveCount(0)
  await expect(page.getByText(NOTICE)).toHaveCount(0)
  const strip = page.locator('[data-check-strip]')
  await expect(strip).toHaveText(/Not matched on TMDB, so the title comes from the file\./)
  await strip.getByRole('button', { name: 'Change' }).click()

  const dialog = page.getByRole('dialog', { name: 'Change match' })
  await expect(dialog.getByRole('searchbox', { name: 'Search TMDB' })).toHaveValue('Dracula A Love Tale')
  await expect(dialog.getByRole('radio', { name: /Not on TMDB/ })).toHaveAttribute('aria-checked', 'true')
  await expect(dialog.getByRole('radio', { name: /^Dracula 2025/ })).toBeVisible()
  expect(log.searches[0].get('q')).toBe('Dracula A Love Tale')
  expect(log.searches[0].get('year')).toBe('2025')

  await dialog.getByRole('radio', { name: /^Dracula 2025/ }).click()
  await dialog.getByRole('button', { name: 'Save' }).click()
  await expect(dialog).toBeHidden()
  expect(log.matches).toEqual([{ tmdb_id: 1246049 }])
  await expect(strip).toHaveCount(0)
})

test('Not on TMDB sends a null match', async ({ page }) => {
  const log = await mockMovie(page, movie({ id: 702, art_url: null, tmdb_id: 6114 }))
  await page.goto('/items/702')
  await page.getByRole('button', { name: 'Change match' }).click()
  const dialog = page.getByRole('dialog', { name: 'Change match' })
  await expect(dialog.getByRole('radio', { name: /Bram Stoker/ })).toHaveAttribute('aria-checked', 'true')
  await dialog.getByRole('radio', { name: /Not on TMDB/ }).click()
  await dialog.getByRole('button', { name: 'Save' }).click()
  await expect(dialog).toBeHidden()
  expect(log.matches).toEqual([{ tmdb_id: null }])
})

test('a year that finds nothing searches the title alone, and an edited search drops the year', async ({ page }) => {
  const log = await mockMovie(page, dracula(), {
    search: (params) => ({ status: 200, body: { results: params.has('year') ? [] : CANDIDATES } }),
  })
  await page.goto('/items/701')
  await page.locator('[data-check-strip]').getByRole('button', { name: 'Change' }).click()
  const dialog = page.getByRole('dialog', { name: 'Change match' })
  await expect(dialog.getByRole('radio', { name: /^Dracula 2025/ })).toBeVisible()
  expect(log.searches.map((p) => p.get('year'))).toEqual(['2025', null])

  await dialog.getByRole('searchbox', { name: 'Search TMDB' }).fill('Dracula')
  await expect.poll(() => log.searches.at(-1)?.get('q')).toBe('Dracula')
  expect(log.searches.at(-1)?.has('year')).toBe(false)
})

test('tmdb_unavailable: the dialog says TMDB is unreachable and a failed save keeps it open', async ({ page }) => {
  const log = await mockMovie(page, dracula(), {
    search: () => ({ status: 502, body: { error: { code: 'tmdb_unavailable', message: 'TMDB could not be reached' } } }),
    match: () => ({ status: 503, body: { error: { code: 'tmdb_unavailable', message: 'TMDB is not configured' } } }),
  })
  await page.goto('/items/701')
  await page.locator('[data-check-strip]').getByRole('button', { name: 'Change' }).click()
  const dialog = page.getByRole('dialog', { name: 'Change match' })
  await expect(dialog.getByText("Can't reach TMDB right now.")).toBeVisible({ timeout: 10_000 })
  await expect(dialog.getByRole('radio', { name: /Not on TMDB/ })).toHaveAttribute('aria-checked', 'true')

  await dialog.getByRole('button', { name: 'Save' }).click()
  await expect(page.getByText("Can't reach TMDB right now. Try again later.")).toBeVisible()
  await expect(dialog).toBeVisible()
  expect(log.matches).toEqual([{ tmdb_id: null }])
})

test('the splash page changes the match from More', async ({ page }) => {
  const log = await mockMovie(page, movie())
  await page.goto('/items/700')
  await page.getByRole('button', { name: 'More' }).click()
  await page.getByRole('menuitem', { name: 'Change match' }).click()
  const dialog = page.getByRole('dialog', { name: 'Change match' })
  await dialog.getByRole('radio', { name: /Bram Stoker/ }).click()
  await dialog.getByRole('button', { name: 'Save' }).click()
  await expect.poll(() => log.matches).toEqual([{ tmdb_id: 6114 }])
})

test('with TMDB not configured no TMDB UI appears and Change opens the edit form', async ({ page }) => {
  const entries = [itemEntry(11, { title: 'Dracula A Love Tale', year: 2025, needs_check: true })]
  const log = await mockMovie(page, dracula(), { configured: false, entries })
  await page.goto('/')
  await expect(page.locator('[data-library-grid] img').first()).toHaveAttribute('src', '/api/items/11/thumb?v=1')

  await page.goto('/items/701')
  const strip = page.locator('[data-check-strip]')
  await expect(strip).toHaveText(/Listed as a movie, based on its file name\./)
  await expect(page.getByText(/TMDB/)).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Change match' })).toHaveCount(0)
  await strip.getByRole('button', { name: 'Change' }).click()
  await expect(page.getByLabel('Title')).toHaveValue('Dracula A Love Tale')
  await expect(page.getByRole('dialog', { name: 'Change match' })).toHaveCount(0)
  expect(log.searches).toHaveLength(0)

  await page.goto('/settings')
  await expect(page.getByRole('heading', { name: 'About' })).toBeVisible()
  await expect(page.getByText(NOTICE)).toHaveCount(0)
})

test('with TMDB not configured an unflagged movie offers no Change match', async ({ page }) => {
  await mockMovie(page, movie({ art_url: null, tmdb_id: null }), { configured: false })
  await page.goto('/items/700')
  await expect(page.getByRole('button', { name: 'Edit item' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Change match' })).toHaveCount(0)
})

test('Settings shows the TMDB notice in About when TMDB is configured', async ({ page }) => {
  await mockMovie(page, movie())
  await page.goto('/settings')
  await expect(page.getByRole('heading', { name: 'About' })).toBeVisible()
  await expect(page.getByText(NOTICE)).toBeVisible()
})
