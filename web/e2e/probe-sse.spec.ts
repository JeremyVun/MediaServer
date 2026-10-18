import { expect, test, type Page } from '@playwright/test'
import type { LibraryEntry } from '../src/api/types.ts'
import { itemEntry, json, mockLibraryApi, showDetail, showEntry, upNext, type LibraryMock } from './support/library.ts'
import { furiousMock, mockPlayerApi } from './support/player.ts'

test.skip(({ browserName }) => browserName !== 'chromium', 'Cache logic is engine independent')

/** Replaces EventSource with one the test drives through `window.__emit`. */
async function fakeEvents(page: Page) {
  await page.addInitScript(() => {
    const sources: EventTarget[] = []
    class FakeEventSource extends EventTarget {
      url: string
      readyState = 1
      onopen: ((e: Event) => void) | null = null
      onerror: ((e: Event) => void) | null = null
      constructor(url: string) {
        super()
        this.url = url
        sources.push(this)
      }
      close() {
        this.readyState = 2
      }
    }
    const w = window as unknown as Record<string, unknown>
    w.EventSource = FakeEventSource
    w.__emit = (type: string, data: unknown) => {
      for (const s of sources) {
        if ((s as FakeEventSource).readyState !== 2) s.dispatchEvent(new MessageEvent(type, { data: JSON.stringify(data) }))
      }
    }
  })
}

const emit = (page: Page, type: string, data: unknown) =>
  page.evaluate(([t, d]) => (window as unknown as { __emit: (t: string, d: unknown) => void }).__emit(t as string, d), [type, data])

function pageErrors(page: Page) {
  const errors: string[] = []
  page.on('pageerror', (error) => errors.push(error.message))
  return errors
}

/** 75 entries (three pages of 30): every fifth a show, the rest movies, one pending episode. */
function deepLibrary(): LibraryMock {
  const entries: LibraryEntry[] = []
  for (let i = 1; i <= 75; i++) {
    if (i % 5 === 0) entries.push(showEntry(i, { title: `Show ${i}`, has_progress: true, up_next: upNext(i * 100 + 1, { title: `Ep ${i}` }) }))
    else if (i === 7) entries.push(itemEntry(i, { type: 'episode', title: 'Andor S01E01' }))
    else entries.push(itemEntry(i, { title: `Movie ${i}` }))
  }
  return { entries, continueWatching: [], collections: [], shows: { 5: showDetail(entries[4] as never) } }
}

async function mockPagedLibrary(page: Page, mock: LibraryMock) {
  const gridRequests: string[] = []
  await mockLibraryApi(page, mock, async (route, path, url) => {
    if (route.request().method() !== 'GET' || path !== '/api/library') return false
    if (url.searchParams.get('in_progress') === '1') return false
    gridRequests.push(url.search)
    const offset = Number(url.searchParams.get('offset') ?? 0)
    const limit = Number(url.searchParams.get('limit') ?? 30)
    await json(route, { total: mock.entries.length, entries: mock.entries.slice(offset, offset + limit) })
    return true
  })
  return gridRequests
}

async function loadEveryPage(page: Page, total: number) {
  const cards = page.locator('[data-library-grid] > *')
  await expect(cards.first()).toBeVisible()
  for (let i = 0; i < 10 && (await cards.count()) < total; i++) {
    await page.mouse.wheel(0, 20_000)
    await page.waitForTimeout(300)
  }
  await expect(cards).toHaveCount(total)
}

// probe: show events and episode item events on a deep-scrolled grid never throw in the cache patches
test('probe: SSE show events on a three-page library never crash', async ({ page }) => {
  const errors = pageErrors(page)
  await fakeEvents(page)
  const mock = deepLibrary()
  await mockPagedLibrary(page, mock)
  await page.goto('/')
  await loadEveryPage(page, 75)

  const episode = { ...mock.entries[6], show: { id: 5, title: 'Show 5' }, season: 1, episode: 1 } as Record<string, unknown>
  delete episode.kind
  await emit(page, 'show.updated', { id: 5 })
  await emit(page, 'show.added', { id: 99 })
  await emit(page, 'show.removed', { id: 10 })
  await emit(page, 'item.updated', episode)
  const movie = { ...mock.entries[0], title: 'Movie 1 renamed' } as Record<string, unknown>
  delete movie.kind
  await emit(page, 'item.updated', movie)
  await emit(page, 'item.removed', { id: 2 })
  await emit(page, 'show.updated', { id: 5 })
  await page.waitForTimeout(1_000)

  expect(errors).toEqual([])
  await expect(page.locator('[data-library-grid] > *').first()).toBeVisible()
})

// probe: one episode progress save (item.updated + show.updated) never refetches every loaded grid page
test('probe: SSE a progress save on a show refetches at most one grid page', async ({ page }) => {
  await fakeEvents(page)
  const mock = deepLibrary()
  const gridRequests = await mockPagedLibrary(page, mock)
  await page.goto('/')
  await loadEveryPage(page, 75)
  await page.waitForTimeout(500)
  const before = gridRequests.length

  // What PUT /api/items/{id}/progress publishes for an episode (SPEC-API "Progress").
  await emit(page, 'item.updated', {
    ...itemEntry(501, { type: 'episode', title: 'Ep 5', show: { id: 5, title: 'Show 5' }, season: 1, episode: 1 }),
    kind: undefined,
    progress: { position_s: 600, completed: false },
  })
  await emit(page, 'show.updated', { id: 5 })
  await page.waitForTimeout(1_500)

  expect(gridRequests.slice(before).length).toBeLessThanOrEqual(1)
})

// probe: when the open show is removed (merged or emptied elsewhere), its page stops showing it
test('probe: SSE show.removed heals an open show page', async ({ page }) => {
  const errors = pageErrors(page)
  await fakeEvents(page)
  const entry = showEntry(5, { title: 'Doomed', has_progress: true, up_next: upNext(501, { title: 'Ep 1' }) })
  const mock: LibraryMock = { entries: [entry], continueWatching: [], collections: [], shows: { 5: showDetail(entry) } }
  await mockLibraryApi(page, mock)
  await page.goto('/shows/5')
  await expect(page.getByRole('heading', { name: 'Doomed' })).toBeVisible()

  delete mock.shows[5]
  await emit(page, 'show.removed', { id: 5 })
  await page.waitForTimeout(1_500)

  expect(errors).toEqual([])
  await expect(page.getByRole('heading', { name: 'Doomed' })).toHaveCount(0)
})

// probe: show events for the playing episode's show never stop playback or the Next card
test('probe: SSE show events during playback keep the player working', async ({ page }) => {
  const errors = pageErrors(page)
  await fakeEvents(page)
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockPlayerApi(page, furiousMock())
  await page.goto('/watch/103')
  const video = page.locator('video')
  await expect.poll(() => video.evaluate((el: HTMLVideoElement) => !el.paused && el.currentTime > 0)).toBe(true)
  await video.evaluate((el: HTMLVideoElement) => {
    el.currentTime = 21
  })
  await page.waitForTimeout(500)

  await emit(page, 'show.updated', { id: 1 })
  await emit(page, 'show.removed', { id: 1 })
  await emit(page, 'show.added', { id: 1 })
  await page.waitForTimeout(500)
  await video.evaluate((el: HTMLVideoElement) => {
    el.currentTime = 30.5
  })
  await expect(page.locator('[data-next-card]')).toBeVisible()
  expect(errors).toEqual([])
  expect(await video.evaluate((el: HTMLVideoElement) => el.paused)).toBe(false)
})
