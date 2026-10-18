import { readFileSync } from 'node:fs'
import { test, type Browser } from '@playwright/test'
import type { ItemDetail } from '../../src/api/types.ts'
import { itemEntry, json, mockLibraryApi, showEntry, upNext, type LibraryMock } from '../support/library'

const IMG = process.env.EXEMPLAR_IMG ?? '/Users/jeremy/projects/media-server/docs/backlog/shows/comps/r3/workshop/img'
const OUT = process.env.CAPTURE_OUT ?? 'capture-out'
const FRAMES = [
  ['phone', { width: 390, height: 844 }],
  ['desktop', { width: 1440, height: 900 }],
] as const
const SCHEMES = ['dark', 'light'] as const

const file = (id: number, rel_path: string) => ({
  id, root_id: 1, rel_path, size: 2.1e9, container: 'matroska',
  duration_s: 3120, width: 1920, height: 1080, bitrate: null, status: 'online' as const, streams: [],
})

const base = {
  year: null,
  summary: null,
  created_at: '2026-01-01 00:00:00',
  updated_at: '2026-01-01 00:00:00',
  deleted_at: null,
  collection_ids: [],
  show: null,
  season: null,
  episode: null,
  episode_end: null,
  needs_check: true,
}

const ITEMS: Record<string, ItemDetail & { thumb: string }> = {
  episode: {
    ...base, id: 501, type: 'episode', title: 'The Wolf Comes Home', thumb_url: '/api/items/501/thumb?v=1', thumb: 'thumbs/12.jpg',
    files: [file(1, 'Furious/Furious.S01E03.1080p.WEB.mkv'), file(2, 'Furious/Furious.S01E03.720p.WEB.mkv')],
    show: { id: 5, title: 'Furious' }, season: 1, episode: 3,
  },
  pending: {
    ...base, id: 502, type: 'episode', title: 'Narkina 5', thumb_url: '/api/items/502/thumb?v=1', thumb: 'thumbs/30.jpg',
    files: [file(3, 'incoming/Star.Wars.Andor.S01E08.Narkina.5.1080p.DSNP.mkv')], season: 1, episode: 8,
  },
  movie: {
    ...base, id: 503, type: 'movie', title: '12 Angry Men', year: 1957, thumb_url: '/api/items/503/thumb?v=1', thumb: 'tmdb/100-backdrop.jpg',
    summary:
      'The defense and the prosecution have rested and the jury is filing into the jury room to decide if a young Spanish-American is guilty or innocent of murdering his father. What begins as an open and shut case soon becomes a mini-drama of each of the jurors’ prejudices and preconceptions about the trial, the accused, and each other.',
    files: [file(4, 'Movies/12.Angry.Men.1957.1080p.BluRay.mkv')], collection_ids: [4],
  },
}

const COLLECTIONS = ['euphoria', 'norsemen', 'movies', 'hotd', 'to watch'].map((name, i) => ({ id: i + 1, name, item_count: 2, thumb_urls: [] }))
const empty: LibraryMock = { entries: [], continueWatching: [], collections: COLLECTIONS, shows: {} }

async function context(browser: Browser, viewport: { width: number; height: number }, scheme: 'dark' | 'light') {
  return browser.newContext({ viewport, deviceScaleFactor: 2, colorScheme: scheme })
}

for (const [name, viewport] of FRAMES) {
  for (const scheme of SCHEMES) {
    test(`item detail ${name} ${scheme}`, async ({ browser }) => {
      const ctx = await context(browser, viewport, scheme)
      const page = await ctx.newPage()
      await mockLibraryApi(page, empty, async (route, path) => {
        for (const item of Object.values(ITEMS)) {
          if (path === `/api/items/${item.id}`) { await json(route, item); return true }
          if (path === `/api/items/${item.id}/thumb`) {
            await route.fulfill({ status: 200, contentType: 'image/jpeg', body: readFileSync(`${IMG}/${item.thumb}`) })
            return true
          }
        }
        if (path === '/api/roots') { await json(route, [{ id: 1, name: 'Media', path: '/Volumes/Media', online: true, free_bytes: 1.2e12, file_count: 291 }]); return true }
        return false
      })
      for (const [key, item] of Object.entries(ITEMS)) {
        await page.goto(`/items/${item.id}`)
        await page.locator('[data-check-strip]').waitFor()
        await page.waitForTimeout(600)
        await page.mouse.move(0, 0)
        await page.screenshot({ path: `${OUT}/item-${key}-${name}-${scheme}.png`, fullPage: true })
      }
      await ctx.close()
    })

    test(`settings ${name} ${scheme}`, async ({ browser }) => {
      const ctx = await context(browser, viewport, scheme)
      const page = await ctx.newPage()
      let available = true
      await mockLibraryApi(page, empty, async (route, path) => {
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
      for (const state of ['on', 'missing']) {
        available = state === 'on'
        await page.goto('/settings')
        const row = page.getByRole('switch', { name: 'Semantic classification' })
        await row.scrollIntoViewIfNeeded()
        await page.evaluate(() => window.scrollBy(0, 200))
        await page.waitForTimeout(400)
        await page.screenshot({ path: `${OUT}/settings-${state}-${name}-${scheme}.png` })
      }
      await ctx.close()
    })

    test(`collection ${name} ${scheme}`, async ({ browser }) => {
      const ctx = await context(browser, viewport, scheme)
      const page = await ctx.newPage()
      const entries = [
        showEntry(1, { title: 'Euphoria', has_progress: true, collection_ids: [1], up_next: upNext(101, { season: 2, episode: 3, progress: { position_s: 900, completed: false }, duration_s: 3300 }) }),
        showEntry(2, { title: 'Last Seen', collection_ids: [1] }),
        itemEntry(10, { title: 'Euphoria special: Rue', type: 'video', collection_ids: [1] }),
      ]
      const art: Record<string, string> = {
        '/api/shows/1/poster': 'tvmaze/euphoria-poster.jpg',
        '/api/shows/2/backdrop': 'tvmaze/lastseen-bg.jpg',
        '/api/items/101/thumb': 'thumbs/40.jpg',
        '/api/items/10/thumb': 'thumbs/41.jpg',
      }
      await mockLibraryApi(page, { ...empty, entries }, async (route, path) => {
        const hit = art[path]
        if (!hit) return false
        await route.fulfill({ status: 200, contentType: 'image/jpeg', body: readFileSync(`${IMG}/${hit}`) })
        return true
      })
      await page.goto('/collections/1')
      await page.locator('img').first().waitFor()
      await page.waitForTimeout(800)
      await page.screenshot({ path: `${OUT}/collection-${name}-${scheme}.png` })
      await ctx.close()
    })
  }
}
