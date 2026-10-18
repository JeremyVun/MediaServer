import { existsSync, mkdirSync, readdirSync, readFileSync, writeFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import { test, type Page, type Route } from '@playwright/test'
import type { ItemSummary, ShowDetail, ShowEpisodeEntry, ShowSeason, UpNext } from '../../src/api/types.ts'
import { itemEntry, mockLibraryApi, showEntry } from '../support/library'

// Show page frames beside the round 3 exemplar, from the exemplar's own data
// and art. CAPTURE=1 CAPTURE_OUT=<dir>; SEAM=1 adds flat-grey frames at every
// 5 % of the scroll plus shots/report.json for the exemplar's seam probe:
//   cd / && python3 <workshop>/tools/seams.py <CAPTURE_OUT>/seam
const WS = process.env.EXEMPLAR_WS ?? '/Users/jeremy/projects/media-server/docs/backlog/shows/comps/r3/workshop'
const OUT = process.env.CAPTURE_OUT ?? 'capture-out'
const SHOW_KEYS = (process.env.SHOW_KEYS ?? 'darkmatter').split(',')

interface ExemplarEpisode {
  item_id?: number
  season: number
  episode: number
  episode_end: number | null
  title: string
  duration_s: number
  available: boolean
  progress?: { position_s: number; completed: boolean; updated_at: string }
  created_at: string
  thumb: string
}
interface ExemplarShow {
  key: string
  title: string
  year: number | null
  tvmaze_id: number | null
  network: string | null
  summary: string | null
  poster: string | null
  background: string | null
  needs_check: string | null
  missing: { season: number; number: number; title: string; airdate: string | null }[]
  episodes: ExemplarEpisode[]
}

const sandbox: { window: { DATA?: { shows: ExemplarShow[] } } } = { window: {} }
runInNewContext(readFileSync(`${WS}/data.js`, 'utf8'), sandbox)
const exemplar = sandbox.window.DATA!.shows

const files = new Map<string, string>()

function toDetail(s: ExemplarShow, id: number): ShowDetail {
  const items = s.episodes.filter((e) => e.item_id != null)
  for (const e of items) files.set(`/api/items/${e.item_id}/thumb`, `${WS}/${e.thumb}`)
  if (s.poster) files.set(`/api/shows/${id}/poster`, `${WS}/${s.poster}`)
  if (s.background) files.set(`/api/shows/${id}/backdrop`, `${WS}/${s.background}`)

  const bySeason = new Map<number, (ShowEpisodeEntry & { n: number })[]>()
  const push = (season: number, entry: ShowEpisodeEntry, n: number) => {
    if (!bySeason.has(season)) bySeason.set(season, [])
    bySeason.get(season)!.push({ ...entry, n } as ShowEpisodeEntry & { n: number })
  }
  for (const e of items) {
    const item: ItemSummary = {
      ...itemEntry(e.item_id!),
      type: 'episode',
      title: e.title,
      year: null,
      duration_s: e.duration_s,
      thumb_url: `/api/items/${e.item_id}/thumb`,
      available: e.available,
      created_at: e.created_at,
      progress: e.progress ? { position_s: e.progress.position_s, completed: e.progress.completed } : undefined,
      show: { id, title: s.title },
      season: e.season,
      episode: e.episode,
      episode_end: e.episode_end,
    }
    push(e.season, { ...item, kind: 'item' }, e.episode)
  }
  for (const m of s.missing) push(m.season, { kind: 'missing', ...m, missing: true }, m.number)
  const seasons: ShowSeason[] = [...bySeason.entries()]
    .sort(([a], [b]) => (a === 0 ? 1 : b === 0 ? -1 : a - b))
    .map(([season, list]) => ({
      season,
      episodes: list.sort((a, b) => a.n - b.n).map(({ n: _n, ...entry }) => (void _n, entry as ShowEpisodeEntry)),
    }))

  const order = [...items].sort((a, b) => (a.season === 0 ? 1 : b.season === 0 ? -1 : a.season - b.season || a.episode - b.episode))
  const started = order.filter((e) => e.progress && !e.progress.completed && e.progress.position_s > 0)
  const done = order.filter((e) => e.progress?.completed)
  const latest = (list: ExemplarEpisode[]) => [...list].sort((a, b) => b.progress!.updated_at.localeCompare(a.progress!.updated_at))[0]
  const lastDone = done.length ? latest(done) : undefined
  const resume = started.length ? latest(started) : undefined
  const pick =
    resume && (!lastDone || resume.progress!.updated_at > lastDone.progress!.updated_at)
      ? resume
      : lastDone
        ? order[order.indexOf(lastDone) + 1]
        : order[0]
  const upNext: UpNext | null = pick
    ? {
        item_id: pick.item_id!,
        season: pick.season,
        episode: pick.episode,
        episode_end: pick.episode_end,
        title: pick.title,
        available: pick.available,
        thumb_url: `/api/items/${pick.item_id}/thumb`,
        progress: pick.progress ? { position_s: pick.progress.position_s, completed: pick.progress.completed } : undefined,
      }
    : null

  const { kind: _kind, ...summary } = showEntry(id, {
    title: s.title,
    year: s.year,
    network: s.network,
    poster_url: s.poster ? `/api/shows/${id}/poster` : null,
    backdrop_url: s.background ? `/api/shows/${id}/backdrop` : null,
    season_count: seasons.filter((x) => x.season > 0).length,
    episode_count: items.length,
    needs_check: s.needs_check != null,
    has_progress: done.length + started.length > 0,
    up_next: upNext,
  })
  void _kind
  return { ...summary, summary: s.summary, tvmaze_id: s.tvmaze_id, season_offset: 0, title_locked: false, seasons }
}

const shows: Record<number, ShowDetail> = {}
SHOW_KEYS.forEach((key, i) => {
  const s = exemplar.find((x) => x.key === key)
  if (!s) throw new Error(`no exemplar show ${key}`)
  shows[i + 1] = toDetail(s, i + 1)
})

// The real TVmaze results for "monster" (exemplar synth.js), served as the
// match-search wire with the exemplar's candidate posters.
const CANDIDATES = [
  [8244, 'Monster', 2004, 'NTV'],
  [32736, 'Monster', 2017, 'NRK1'],
  [13728, 'Monster', 2016, 'MBC'],
  [17890, 'American Monster', 2016, 'Investigation Discovery'],
  [6937, 'Monster High', 2010, 'Nickelodeon'],
] as const

async function open(page: Page, flat: boolean) {
  await page.route('https://api.tvmaze.com/**', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', headers: { 'access-control-allow-origin': '*' }, body: '[{"number":1}]' }),
  )
  await mockLibraryApi(page, { entries: [], continueWatching: [], collections: [], shows }, async (route: Route, path) => {
    if (path === '/api/shows/match-search') {
      const results = CANDIDATES.map(([id, name, year, network]) => ({ tvmaze_id: id, name, year, network, poster_url: `/api/cand/${id}`, local_show_id: null }))
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ results }) })
      return true
    }
    if (path === '/api/library') {
      const entries = Object.values(shows).map((s) => ({ kind: 'show', ...s }))
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ total: entries.length, entries }) })
      return true
    }
    const candidate = path.match(/^\/api\/cand\/(\d+)$/)
    if (candidate) {
      await route.fulfill({ status: 200, contentType: 'image/jpeg', body: readFileSync(`${WS}/img/tvmaze-cand/${candidate[1]}.jpg`) })
      return true
    }
    const file = files.get(path)
    if (!file) return false
    if (flat && path.endsWith('/backdrop')) {
      const svg = '<svg xmlns="http://www.w3.org/2000/svg" width="1920" height="1080"><rect width="1920" height="1080" fill="rgb(122,122,122)"/></svg>'
      await route.fulfill({ status: 200, contentType: 'image/svg+xml', body: svg })
      return true
    }
    await route.fulfill({ status: 200, contentType: 'image/jpeg', body: readFileSync(file) })
    return true
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

for (const [name, viewport] of FRAMES) {
  for (const scheme of ['dark', 'light'] as const) {
    test(`show ${name} ${scheme}`, async ({ browser }) => {
      const context = await browser.newContext({ viewport, deviceScaleFactor: 2, colorScheme: scheme, reducedMotion: 'no-preference' })
      const page = await context.newPage()
      await open(page, false)
      for (const [i, key] of SHOW_KEYS.entries()) {
        await page.goto(`/shows/${i + 1}`)
        await page.locator('[data-tiles] img').first().waitFor()
        await page.waitForTimeout(800)
        for (const frac of [0, 0.25]) {
          await scrollTo(page, frac)
          await page.screenshot({ path: `${OUT}/s-${key}-${name}-${scheme}-${frac * 100}.png` })
        }
      }
      await context.close()
    })
  }
}

for (const [name, viewport] of FRAMES) {
  test(`dialogs ${name} dark`, async ({ browser }) => {
    const context = await browser.newContext({ viewport, deviceScaleFactor: 2, colorScheme: 'dark' })
    const page = await context.newPage()
    await open(page, false)
    await page.goto('/shows/1')
    await page.locator('[data-tiles] img').first().waitFor()
    await page.getByRole('button', { name: 'More' }).click()
    await page.getByRole('menuitem', { name: 'Change match' }).click()
    const change = page.getByRole('dialog', { name: 'Change match' })
    await change.getByRole('radio').nth(1).waitFor()
    await page.waitForTimeout(600)
    await page.screenshot({ path: `${OUT}/change-${name}-dark.png` })
    await change.getByRole('radio').first().click()
    await change.getByRole('combobox').waitFor()
    await page.screenshot({ path: `${OUT}/change-picked-${name}-dark.png` })
    await change.getByRole('button', { name: 'Cancel' }).click()
    await page.getByRole('button', { name: 'More' }).click()
    await page.getByRole('menuitem', { name: 'Merge into another show' }).click()
    await page.getByRole('dialog', { name: 'Merge into another show' }).getByRole('radio').first().waitFor()
    await page.waitForTimeout(400)
    await page.screenshot({ path: `${OUT}/merge-${name}-dark.png` })
    await context.close()
  })
}

if (process.env.SEAM) {
  test.afterAll(() => {
    const dir = `${OUT}/seam/shots`
    if (!existsSync(dir)) return
    const parts = readdirSync(dir).filter((f) => /^report-.+\.json$/.test(f))
    const merged = Object.assign({}, ...parts.map((f) => JSON.parse(readFileSync(`${dir}/${f}`, 'utf8'))))
    writeFileSync(`${dir}/report.json`, JSON.stringify(merged))
  })
  for (const [name, viewport] of FRAMES) {
    for (const scheme of ['dark', 'light'] as const) {
      test(`seam ${name} ${scheme}`, async ({ browser }) => {
        test.setTimeout(120_000)
        const dir = `${OUT}/seam/shots`
        mkdirSync(dir, { recursive: true })
        const context = await browser.newContext({ viewport, deviceScaleFactor: 1, colorScheme: scheme, reducedMotion: 'no-preference' })
        const page = await context.newPage()
        await open(page, true)
        await page.goto('/shows/1')
        await page.locator('[data-tiles] img').first().waitFor()
        await page.waitForTimeout(800)
        const report: Record<string, unknown> = {}
        for (let pct = 0; pct <= 100; pct += 5) {
          await scrollTo(page, pct / 100)
          const sid = `s-flat-${name}-${scheme}-${String(pct).padStart(3, '0')}`
          await page.screenshot({ path: `${dir}/${sid}.png` })
          report[sid] = { ...(await page.evaluate(contentBoxes)), scheme, frame: name }
        }
        writeFileSync(`${dir}/report-${name}-${scheme}.json`, JSON.stringify(report))
        await context.close()
      })
    }
  }
}

/** Every content box in view, so the probe measures only the backdrop. */
function contentBoxes() {
  const vh = innerHeight
  const content: number[][] = []
  const push = (r: DOMRect) => {
    if (r.width > 0 && r.height > 0 && r.bottom > 0 && r.top < vh) {
      content.push([Math.round(r.left), Math.round(r.top), Math.round(r.width), Math.round(r.height)])
    }
  }
  for (const el of document.querySelectorAll('[data-tiles] > *, [data-check-strip], a, button, input, [role="tab"], img')) {
    if (el.closest('[data-splash]') || el.getAttribute('aria-hidden') === 'true') continue
    push(el.getBoundingClientRect())
  }
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    if (!n.textContent?.trim() || n.parentElement?.closest('[data-splash]')) continue
    const range = document.createRange()
    range.selectNodeContents(n)
    for (const r of range.getClientRects()) push(r)
  }
  return { viewport: { w: innerWidth, h: vh }, scroll: { y: Math.round(scrollY) }, content }
}
