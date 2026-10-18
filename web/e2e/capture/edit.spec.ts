import { readFileSync } from 'node:fs'
import { test } from '@playwright/test'
import type { ItemDetail } from '../../src/api/types.ts'
import { json, mockLibraryApi } from '../support/library'

const IMG = process.env.EXEMPLAR_IMG ?? '/Users/jeremy/projects/media-server/docs/backlog/shows/comps/r3/workshop/img'
const OUT = process.env.CAPTURE_OUT ?? 'capture-out'
const phone = { viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, colorScheme: 'dark' as const }

const episode: ItemDetail = {
  id: 501,
  type: 'episode',
  title: 'The Wolf Comes Home',
  year: null,
  summary: null,
  created_at: '2026-01-01 00:00:00',
  updated_at: '2026-01-01 00:00:00',
  deleted_at: null,
  collection_ids: [],
  thumb_url: '/api/items/501/thumb?v=1',
  files: [
    {
      id: 1, root_id: 1, rel_path: 'Furious/Furious.S01E03.1080p.WEB.mkv', size: 2.1e9, container: 'matroska',
      duration_s: 3120, width: 1920, height: 1080, bitrate: null, status: 'online', streams: [],
    },
  ],
  show: { id: 5, title: 'Furious' },
  season: 1,
  episode: 3,
  episode_end: null,
  needs_check: true,
}

test('item detail episode phone', async ({ browser }) => {
  const context = await browser.newContext(phone)
  const page = await context.newPage()
  await mockLibraryApi(page, { entries: [], continueWatching: [], collections: [], shows: {} }, async (route, path) => {
    if (path === '/api/items/501') { await json(route, episode); return true }
    if (path === '/api/items/501/thumb') {
      await route.fulfill({ status: 200, contentType: 'image/jpeg', body: readFileSync(`${IMG}/thumbs/12.jpg`) })
      return true
    }
    return false
  })
  await page.goto('/items/501')
  await page.locator('[data-check-strip]').waitFor()
  await page.waitForTimeout(600)
  await page.screenshot({ path: `${OUT}/item-episode-phone-dark.png`, fullPage: true })
  await context.close()
})

for (const available of [true, false]) {
  test(`settings phone ${available ? 'on' : 'missing'}`, async ({ browser }) => {
    const context = await browser.newContext(phone)
    const page = await context.newPage()
    await mockLibraryApi(page, { entries: [], continueWatching: [], collections: [], shows: {} }, async (route, path) => {
      if (path === '/api/settings') {
        await json(route, {
          hls_cache: { dir: '/Volumes/Media/.hls', max_bytes: 5e10, used_bytes: 1e9, available: true },
          semantic_classification: { enabled: available, available },
        })
        return true
      }
      if (path === '/api/roots') {
        await json(route, [{ id: 1, name: 'Media', path: '/Volumes/Media', online: true, free_bytes: 1.2e12, file_count: 291 }])
        return true
      }
      if (path === '/api/jobs') { await json(route, []); return true }
      if (path === '/api/items') { await json(route, { total: 0, items: [] }); return true }
      return false
    })
    await page.goto('/settings')
    const row = page.getByRole('switch', { name: 'Semantic classification' })
    await row.scrollIntoViewIfNeeded()
    await page.evaluate(() => window.scrollBy(0, 200))
    await page.waitForTimeout(400)
    await page.screenshot({ path: `${OUT}/settings-${available ? 'on' : 'missing'}-phone-dark.png` })
    await context.close()
  })
}
