import { expect, test, type Page } from '@playwright/test'
import type { ItemDetail, ItemPatch, SettingsResponse } from '../src/api/types.ts'
import { itemEntry, json, mockLibraryApi, showDetail, showEntry, type LibraryMock } from './support/library.ts'

const furious = showEntry(5, { title: 'Furious', episode_count: 3 })
const andor = showEntry(9, { title: 'Andor', episode_count: 12 })

function itemDetail(overrides: Partial<ItemDetail> = {}): ItemDetail {
  return {
    id: 501,
    type: 'episode',
    title: 'Pilot',
    year: null,
    summary: null,
    created_at: '2026-01-01 00:00:00',
    updated_at: '2026-01-01 00:00:00',
    deleted_at: null,
    collection_ids: [],
    thumb_url: '/api/items/501/thumb?v=1',
    files: [
      {
        id: 1,
        root_id: 1,
        rel_path: 'Furious/Furious.S01E03.mkv',
        size: 1e9,
        container: 'matroska',
        duration_s: 3000,
        width: 1920,
        height: 1080,
        bitrate: null,
        status: 'online',
        streams: [],
      },
    ],
    show: { id: 5, title: 'Furious' },
    season: 1,
    episode: 3,
    episode_end: null,
    needs_check: true,
    ...overrides,
  }
}

function baseMock(): LibraryMock {
  return {
    entries: [],
    continueWatching: [],
    collections: [],
    shows: { 5: showDetail(furious), 9: showDetail(andor) },
  }
}

/** Serves one item detail; PATCHes are logged and answered by `answer`. */
async function mockItem(
  page: Page,
  initial: ItemDetail,
  answer: (patch: ItemPatch, item: ItemDetail) => { status: number; body: unknown } = (patch, item) => ({
    status: 200,
    body: { ...item, ...patch, ...(patch.confirmed ? { needs_check: false } : {}), updated_at: 'later' },
  }),
) {
  let item = initial
  const patches: ItemPatch[] = []
  await mockLibraryApi(page, baseMock(), async (route, path, url) => {
    const method = route.request().method()
    if (path === `/api/items/${item.id}` && method === 'GET') {
      await json(route, item)
      return true
    }
    if (path === `/api/items/${item.id}` && method === 'PATCH') {
      const patch = route.request().postDataJSON() as ItemPatch
      patches.push(patch)
      const { status, body } = answer(patch, item)
      if (status === 200) item = body as ItemDetail
      await json(route, body, status)
      return true
    }
    if (path === '/api/library' && url.searchParams.get('type') === 'show') {
      await json(route, { total: 2, entries: [andor, furious] })
      return true
    }
    return false
  })
  return patches
}

test('an episode shows its show and place, and Looks right confirms it', async ({ page }) => {
  const patches = await mockItem(page, itemDetail())
  await page.goto('/items/501')

  await expect(page.getByRole('link', { name: 'Furious' })).toHaveAttribute('href', '/shows/5')
  await expect(page.getByText('· Season 1 · Episode 3')).toBeVisible()
  const strip = page.locator('[data-check-strip]')
  await expect(strip).toHaveText(/Added to Furious as S1 E3\./)
  await strip.getByRole('button', { name: 'Looks right' }).click()
  await expect(strip).toHaveCount(0)
  expect(patches).toEqual([{ confirmed: true }])
})

test('Change on an episode moves it with only show_id', async ({ page }) => {
  const patches = await mockItem(page, itemDetail(), (patch, item) => ({
    status: 200,
    body: { ...item, show: { id: 9, title: 'Andor' }, needs_check: false, updated_at: 'later' },
  }))
  await page.goto('/items/501')

  await page.locator('[data-check-strip]').getByRole('button', { name: 'Change' }).click()
  const dialog = page.getByRole('dialog', { name: 'Move to show' })
  await expect(dialog.getByRole('radio', { name: /Furious/ })).toHaveCount(0)
  await dialog.getByRole('radio', { name: /Andor/ }).click()
  await dialog.getByRole('button', { name: 'Move' }).click()
  await expect(dialog).toBeHidden()
  expect(patches).toEqual([{ show_id: 9 }])
  await expect(page.getByRole('link', { name: 'Andor' })).toBeVisible()
})

test('Move to show sits beside an unflagged episode', async ({ page }) => {
  const patches = await mockItem(page, itemDetail({ needs_check: false }))
  await page.goto('/items/501')
  await expect(page.locator('[data-check-strip]')).toHaveCount(0)
  await page.getByRole('button', { name: 'Move to show' }).click()
  await page.getByRole('dialog', { name: 'Move to show' }).getByRole('radio', { name: /Andor/ }).click()
  await page.getByRole('dialog', { name: 'Move to show' }).getByRole('button', { name: 'Move' }).click()
  await expect.poll(() => patches).toEqual([{ show_id: 9 }])
})

test('editing season and episode sends only the changed numbers', async ({ page }) => {
  const patches = await mockItem(page, itemDetail({ needs_check: false }))
  await page.goto('/items/501')
  await page.getByRole('button', { name: 'Edit item' }).click()
  await expect(page.getByLabel('Season')).toHaveValue('1')
  await page.getByLabel('Episode', { exact: true }).fill('4')
  await page.getByRole('button', { name: 'Save' }).click()
  await expect.poll(() => patches).toEqual([{ episode: 4 }])
})

test('choosing movie on an episode sends just the type', async ({ page }) => {
  const patches = await mockItem(page, itemDetail({ needs_check: false }), (patch, item) => ({
    status: 200,
    body: { ...item, type: 'movie', show: null, season: null, episode: null, updated_at: 'later' },
  }))
  await page.goto('/items/501')
  await page.getByRole('button', { name: 'Edit item' }).click()
  await page.getByLabel('Type').selectOption('movie')
  await expect(page.getByLabel('Season')).toHaveCount(0)
  await page.getByRole('button', { name: 'Save' }).click()
  await expect.poll(() => patches).toEqual([{ type: 'movie' }])
  await expect(page.getByText('Movie', { exact: true })).toBeVisible()
})

test('choosing episode on a movie requires a show and sends type with show_id', async ({ page }) => {
  let rejected = false
  const movie = itemDetail({ id: 600, type: 'movie', title: 'Zodiac', year: 2007, show: null, season: null, episode: null, needs_check: false })
  const patches = await mockItem(page, movie, (patch, item) => {
    if (!rejected) {
      rejected = true
      return { status: 400, body: { error: { code: 'invalid_episode', message: 'an episode needs a show' } } }
    }
    return { status: 200, body: { ...item, ...patch, show: { id: 9, title: 'Andor' }, updated_at: 'later' } }
  })
  await page.goto('/items/600')
  await page.getByRole('button', { name: 'Edit item' }).click()

  await page.getByLabel('Type').selectOption('episode')
  const picker = page.getByRole('dialog', { name: 'Choose a show' })
  await expect(picker).toBeVisible()
  await picker.getByRole('button', { name: 'Cancel' }).click()
  await expect(page.getByLabel('Type')).toHaveValue('movie')

  await page.getByLabel('Type').selectOption('episode')
  await picker.getByRole('radio', { name: /Andor/ }).click()
  await picker.getByRole('button', { name: 'Choose' }).click()
  await expect(picker).toBeHidden()
  await page.getByLabel('Season').fill('2')
  await page.getByLabel('Episode', { exact: true }).fill('5')
  await page.getByRole('button', { name: 'Save' }).click()

  await expect(page.getByText('Choose a show for this episode.')).toBeVisible()
  await expect(picker).toBeVisible()
  await picker.getByRole('radio', { name: /Andor/ }).click()
  await picker.getByRole('button', { name: 'Choose' }).click()
  await page.getByRole('button', { name: 'Save' }).click()

  const expected = { type: 'episode', show_id: 9, season: 2, episode: 5 }
  await expect.poll(() => patches).toEqual([expected, expected])
  await expect(page.getByRole('link', { name: 'Andor' })).toBeVisible()
})

test('a flagged movie confirms with Looks right and shows its summary clamped', async ({ page }) => {
  const summary = 'A cartoonist becomes obsessed with the Zodiac killer. '.repeat(12)
  const patches = await mockItem(
    page,
    itemDetail({ id: 600, type: 'movie', title: 'Zodiac', summary, show: null, season: null, episode: null }),
  )
  await page.goto('/items/600')
  const toggle = page.getByRole('button', { name: /A cartoonist/ })
  await expect(toggle).toHaveAttribute('aria-expanded', 'false')
  await toggle.click()
  await expect(toggle).toHaveAttribute('aria-expanded', 'true')

  const strip = page.locator('[data-check-strip]')
  await expect(strip).toHaveText(/Listed as a movie, based on its file name\./)
  await strip.getByRole('button', { name: 'Looks right' }).click()
  await expect(strip).toHaveCount(0)
  expect(patches).toEqual([{ confirmed: true }])
})

test('item detail has one amber accent: Play, not the file row or collection chips', async ({ page }) => {
  const second = { ...itemDetail().files[0], id: 2, rel_path: 'Furious/Furious.S01E03.720p.mkv' }
  await mockItem(page, itemDetail({ files: [itemDetail().files[0], second], collection_ids: [3] }))
  await page.route(
    (url) => url.pathname === '/api/collections',
    (route) => json(route, [{ id: 3, name: 'to watch', item_count: 1, thumb_urls: [] }]),
  )
  await page.goto('/items/501')
  await expect(page.getByRole('button', { name: 'to watch' })).toBeVisible()
  await expect(page.getByText('Selected', { exact: true })).toBeVisible()
  await page.mouse.move(0, 0)

  const amber = await page.evaluate(() => {
    const accent = /rgb\((232, 163, 61|199, 126, 20)\)/
    const hits: string[] = []
    for (const el of document.querySelectorAll<HTMLElement>('main *')) {
      if (!el.checkVisibility()) continue
      const cs = getComputedStyle(el)
      const border = parseFloat(cs.borderTopWidth) > 0 && accent.test(cs.borderTopColor)
      const outline = cs.outlineStyle !== 'none' && accent.test(cs.outlineColor)
      if (accent.test(cs.backgroundColor) || border || outline || accent.test(cs.boxShadow)) hits.push(el.textContent?.trim() ?? el.tagName)
    }
    return hits
  })
  expect(amber).toEqual(['Play'])
})

async function mockSettings(page: Page, available: boolean) {
  let settings: SettingsResponse = {
    hls_cache: { dir: '/tmp/hls', max_bytes: 1e10, used_bytes: 0, available: true },
    semantic_classification: { enabled: false, available },
  }
  const puts: unknown[] = []
  await mockLibraryApi(page, baseMock(), async (route, path) => {
    const method = route.request().method()
    if (path === '/api/settings' && method === 'GET') {
      await json(route, settings)
      return true
    }
    if (path === '/api/settings/semantic-classification' && method === 'PUT') {
      const body = route.request().postDataJSON() as { enabled: boolean }
      puts.push(body)
      settings = { ...settings, semantic_classification: { enabled: body.enabled, available } }
      await json(route, settings)
      return true
    }
    if (path === '/api/roots' || path === '/api/jobs') {
      await json(route, [])
      return true
    }
    if (path === '/api/items') {
      await json(route, { total: 0, items: [] })
      return true
    }
    return false
  })
  return puts
}

test('the semantic classification switch turns on', async ({ page }) => {
  const puts = await mockSettings(page, true)
  await page.goto('/settings')
  const toggle = page.getByRole('switch', { name: 'Semantic classification' })
  await expect(toggle).toHaveAttribute('aria-checked', 'false')
  await expect(toggle).toBeEnabled()
  await toggle.click()
  await expect(toggle).toHaveAttribute('aria-checked', 'true')
  expect(puts).toEqual([{ enabled: true }])
  await expect(page.getByText("Codex isn't available on this Mac.", { exact: false })).toHaveCount(0)
})

test('the switch is disabled with a reason when Codex is missing', async ({ page }) => {
  const puts = await mockSettings(page, false)
  await page.goto('/settings')
  const toggle = page.getByRole('switch', { name: 'Semantic classification' })
  await expect(toggle).toBeDisabled()
  await expect(toggle).toHaveAttribute('aria-checked', 'false')
  await expect(page.getByText("Codex isn't available on this Mac. Install Codex to turn this on.")).toBeVisible()
  await page.getByText('Semantic classification', { exact: true }).click({ force: true })
  expect(puts).toEqual([])
})

test('a collection lists shows beside items, reorders by entries and removes a show', async ({ page }) => {
  const zodiac = itemEntry(10, { title: 'Zodiac', collection_ids: [1] })
  const show = showEntry(5, { title: 'Furious', collection_ids: [1] })
  const collection = { id: 1, name: 'To watch', item_count: 2, thumb_urls: [] }
  let order: string[] = ['item:10', 'show:5']
  const orders: unknown[] = []
  const removals: string[] = []
  await mockLibraryApi(
    page,
    { ...baseMock(), entries: [zodiac, show], collections: [collection] },
    async (route, path, url) => {
      const method = route.request().method()
      if (path === '/api/library' && url.searchParams.get('collection_id') === '1') {
        const byKey = { 'item:10': zodiac, 'show:5': show } as const
        const entries = order.map((key) => byKey[key as keyof typeof byKey])
        await json(route, { total: entries.length, entries })
        return true
      }
      if (path === '/api/collections/1/order' && method === 'PUT') {
        const body = route.request().postDataJSON() as { entries: ({ item_id: number } | { show_id: number })[] }
        orders.push(body)
        order = body.entries.map((e) => ('show_id' in e ? `show:${e.show_id}` : `item:${e.item_id}`))
        await route.fulfill({ status: 204 })
        return true
      }
      if (method === 'DELETE' && path.startsWith('/api/collections/1/')) {
        removals.push(path)
        order = order.filter((key) => key !== 'show:5')
        await route.fulfill({ status: 204 })
        return true
      }
      return false
    },
  )

  await page.goto('/collections/1')
  const grid = page.locator('[data-collection-grid]')
  await expect(grid.getByRole('link', { name: 'Open Furious' })).toHaveAttribute('href', '/shows/5')
  await expect(grid.getByRole('link', { name: 'Play Zodiac' })).toBeVisible()
  await expect(page.getByText('1 show and 1 item')).toBeVisible()

  await grid.getByRole('button', { name: 'Move later' }).first().click()
  await expect.poll(() => orders).toEqual([{ entries: [{ show_id: 5 }, { item_id: 10 }] }])

  await grid.getByRole('button', { name: 'Remove Furious from collection' }).click()
  await expect.poll(() => removals).toEqual(['/api/collections/1/shows/5'])
  await expect(grid.getByRole('link', { name: 'Open Furious' })).toHaveCount(0)
})
