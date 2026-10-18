import { existsSync, mkdirSync, readdirSync, readFileSync, writeFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import { test, type Page } from '@playwright/test'
import type { ItemDetail } from '../../src/api/types.ts'
import { itemEntry, json, mockLibraryApi } from '../support/library'
import { contentBoxes, photoText } from './probes'

// Movie frames beside the round 3 exemplar's s-movie-* shots, from its own
// data and TMDB art. CAPTURE=1 CAPTURE_OUT=<dir>; SEAM=1 adds flat-grey
// frames of the 12 Angry Men page at every 5 % of the scroll, CONTRAST=1 the
// text-over-art frames (probes as in show.spec.ts).
const WS = process.env.EXEMPLAR_WS ?? '/Users/jeremy/projects/media-server/docs/backlog/shows/comps/r3/workshop'
const OUT = process.env.CAPTURE_OUT ?? 'capture-out'

interface ExemplarMovie {
  id: number
  title: string
  year: number | null
  duration_s: number
  available: boolean
  created_at: string
  progress: { position_s: number; completed: boolean } | null
  thumb: string
  raw_title: string
  tmdb_id?: number
  overview?: string
  backdrop?: string
  needs_check?: string
  tmdb_candidates?: { tmdb_id: number; title: string; year: string | null; poster: string | null }[]
}

const sandbox: { window: { DATA?: { movies: ExemplarMovie[] } } } = { window: {} }
runInNewContext(readFileSync(`${WS}/data.js`, 'utf8'), sandbox)
const movies = sandbox.window.DATA!.movies
const byID = new Map(movies.map((m) => [m.id, m]))

const PAGES = { '12-angry-men': 234, eeaao: 96, unmatched: 299 } as const

function detail(m: ExemplarMovie): ItemDetail {
  const { kind: _kind, ...summary } = entry(m)
  void _kind
  return {
    ...summary,
    summary: m.overview ?? null,
    updated_at: m.created_at,
    deleted_at: null,
    files: [
      {
        id: m.id, root_id: 1, rel_path: m.raw_title, size: 2.1e9, container: 'matroska',
        duration_s: m.duration_s, width: 1920, height: 1080, bitrate: null, status: m.available ? 'online' : 'offline', streams: [],
      },
    ],
  }
}

function entry(m: ExemplarMovie) {
  return itemEntry(m.id, {
    title: m.title,
    year: m.year,
    duration_s: m.duration_s,
    available: m.available,
    created_at: m.created_at,
    progress: m.progress ? { position_s: m.progress.position_s, completed: m.progress.completed } : undefined,
    thumb_url: `/api/items/${m.id}/thumb`,
    art_url: m.backdrop ? `/api/items/${m.id}/art` : null,
    tmdb_id: m.tmdb_id ?? null,
    needs_check: m.needs_check != null,
  })
}

const grey = (w: number, h: number) =>
  `<svg xmlns="http://www.w3.org/2000/svg" width="${w}" height="${h}"><rect width="${w}" height="${h}" fill="rgb(122,122,122)"/></svg>`

async function open(page: Page, flat = false) {
  const entries = [...movies].sort((a, b) => b.created_at.localeCompare(a.created_at)).map(entry)
  await mockLibraryApi(page, { entries, continueWatching: [], collections: [], shows: {} }, async (route, path) => {
    if (path === '/api/settings') {
      await json(route, {
        hls_cache: { dir: '/Volumes/Media/.hls', max_bytes: 5e10, used_bytes: 1e9, available: true },
        semantic_classification: { enabled: false, available: true },
        tmdb: { configured: true },
      })
      return true
    }
    let hit = path.match(/^\/api\/items\/(\d+)(\/(thumb|art|match-search))?$/)
    const m = hit && byID.get(Number(hit[1]))
    if (hit && m) {
      if (!hit[2]) await json(route, detail(m))
      else if (hit[3] === 'match-search') {
        const results = (m.tmdb_candidates ?? []).map((c) => ({
          tmdb_id: c.tmdb_id, title: c.title, year: c.year ? Number(c.year) : null, poster_url: c.poster ? `/api/tmdb-poster/${c.tmdb_id}` : null,
        }))
        await json(route, { results })
      } else if (hit[3] === 'art' && flat) {
        await route.fulfill({ status: 200, contentType: 'image/svg+xml', body: grey(1280, 720) })
      } else {
        const file = hit[3] === 'art' ? m.backdrop : m.thumb
        if (!file) return false
        await route.fulfill({ status: 200, contentType: 'image/jpeg', body: readFileSync(`${WS}/${file}`) })
      }
      return true
    }
    hit = path.match(/^\/api\/tmdb-poster\/(\d+)$/)
    if (hit) {
      const poster = movies.flatMap((x) => x.tmdb_candidates ?? []).find((c) => c.tmdb_id === Number(hit![1]))?.poster
      if (!poster) return false
      await route.fulfill({ status: 200, contentType: 'image/jpeg', body: readFileSync(`${WS}/${poster}`) })
      return true
    }
    return false
  })
}

async function scrollTo(page: Page, frac: number) {
  await page.evaluate((f) => {
    window.scrollTo(0, Math.round((document.documentElement.scrollHeight - innerHeight) * f))
  }, frac)
  await page.waitForTimeout(250)
}

const FRAMES = [
  ['phone', { width: 390, height: 844 }],
  ['desktop', { width: 1440, height: 900 }],
] as const
const SCHEMES = ['dark', 'light'] as const

for (const [name, viewport] of FRAMES) {
  for (const scheme of SCHEMES) {
    test(`movies ${name} ${scheme}`, async ({ browser }) => {
      const context = await browser.newContext({ viewport, deviceScaleFactor: 2, colorScheme: scheme, reducedMotion: 'no-preference' })
      const page = await context.newPage()
      await open(page)

      await page.goto('/?type=movie')
      await page.locator('[data-library-grid] img').first().waitFor()
      await page.waitForTimeout(1200)
      await page.screenshot({ path: `${OUT}/movies-tab-${name}-${scheme}.png` })
      const card = page.getByRole('link', { name: 'Open Dracula A Love Tale' }).or(page.getByRole('link', { name: 'Play Dracula A Love Tale' }))
      await card.scrollIntoViewIfNeeded()
      await page.waitForTimeout(400)
      await card.screenshot({ path: `${OUT}/moviecard-unmatched-${name}-${scheme}.png` })
      await page.getByRole('link', { name: 'Play 12 Angry Men' }).screenshot({ path: `${OUT}/moviecard-${name}-${scheme}.png` })

      for (const [key, id] of Object.entries(PAGES)) {
        await page.goto(`/items/${id}`)
        await page.getByRole('heading', { level: 1 }).waitFor()
        await page.waitForTimeout(900)
        await page.mouse.move(0, 0)
        await page.screenshot({ path: `${OUT}/movie-${key}-${name}-${scheme}.png` })
        if (key === '12-angry-men') {
          for (const frac of [0.25, 0.5, 1]) {
            await scrollTo(page, frac)
            await page.screenshot({ path: `${OUT}/movie-seq-${name}-${scheme}-${frac * 100}.png` })
          }
        }
      }

      await page.goto(`/items/${PAGES.unmatched}`)
      await page.locator('[data-check-strip]').getByRole('button', { name: 'Change' }).click()
      await page.getByRole('dialog', { name: 'Change match' }).getByRole('radio').nth(1).waitFor()
      await page.waitForTimeout(700)
      await page.screenshot({ path: `${OUT}/movie-dialog-${name}-${scheme}.png` })
      await context.close()
    })
  }
}

if (process.env.SEAM) {
  test.afterAll(() => {
    const dir = `${OUT}/seam/shots`
    if (!existsSync(dir)) return
    const parts = readdirSync(dir).filter((f) => /^report-.+\.json$/.test(f))
    writeFileSync(`${dir}/report.json`, JSON.stringify(Object.assign({}, ...parts.map((f) => JSON.parse(readFileSync(`${dir}/${f}`, 'utf8'))))))
  })
  for (const [name, viewport] of FRAMES) {
    for (const scheme of SCHEMES) {
      test(`seam ${name} ${scheme}`, async ({ browser }) => {
        test.setTimeout(120_000)
        const dir = `${OUT}/seam/shots`
        mkdirSync(dir, { recursive: true })
        const context = await browser.newContext({ viewport, deviceScaleFactor: 1, colorScheme: scheme, reducedMotion: 'no-preference' })
        const page = await context.newPage()
        await open(page, true)
        await page.goto(`/items/${PAGES['12-angry-men']}`)
        await page.locator('[data-splash] img').waitFor()
        await page.waitForTimeout(800)
        const report: Record<string, unknown> = {}
        for (let pct = 0; pct <= 100; pct += 5) {
          await scrollTo(page, pct / 100)
          const sid = `s-flat-movie-${name}-${scheme}-${String(pct).padStart(3, '0')}`
          await page.screenshot({ path: `${dir}/${sid}.png` })
          report[sid] = { ...(await page.evaluate(contentBoxes)), scheme, frame: name }
        }
        writeFileSync(`${dir}/report-${name}-${scheme}.json`, JSON.stringify(report))
        await context.close()
      })
    }
  }
}

if (process.env.CONTRAST) {
  test.afterAll(() => {
    const dir = `${OUT}/contrast/shots`
    if (!existsSync(dir)) return
    const parts = readdirSync(dir).filter((f) => /^report-.+\.json$/.test(f))
    writeFileSync(`${dir}/report.json`, JSON.stringify(Object.assign({}, ...parts.map((f) => JSON.parse(readFileSync(`${dir}/${f}`, 'utf8'))))))
  })
  for (const [name, viewport] of FRAMES) {
    for (const scheme of SCHEMES) {
      test(`contrast ${name} ${scheme}`, async ({ browser }) => {
        test.setTimeout(120_000)
        const dir = `${OUT}/contrast/shots`
        mkdirSync(dir, { recursive: true })
        const context = await browser.newContext({ viewport, deviceScaleFactor: 2, colorScheme: scheme, reducedMotion: 'no-preference' })
        const page = await context.newPage()
        await open(page)
        const report: Record<string, unknown> = {}
        for (const key of ['12-angry-men', 'eeaao'] as const) {
          await page.goto(`/items/${PAGES[key]}`)
          await page.locator('[data-splash] img').waitFor()
          await page.waitForTimeout(800)
          for (const frac of [0, 0.25]) {
            await scrollTo(page, frac)
            const sid = `s-contrast-movie-${key}-${name}-${scheme}-${frac * 100}`
            await page.screenshot({ path: `${dir}/${sid}.png` })
            report[sid] = { photoText: await page.evaluate(photoText), viewport: { ...viewport, dpr: 2 }, scheme, frame: name }
          }
        }
        writeFileSync(`${dir}/report-${name}-${scheme}.json`, JSON.stringify(report))
        await context.close()
      })
    }
  }
}
