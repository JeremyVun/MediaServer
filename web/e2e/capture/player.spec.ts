import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import { test, type Browser, type Page } from '@playwright/test'
import type { ShowEpisodeEntry, ShowSeason } from '../../src/api/types.ts'
import { showDetail, showEntry, type ItemEntry } from '../support/library'
import { episode, missingEpisode, mockPlayerApi, playerFixture, type PlayerMock } from '../support/player'

const WS = process.env.EXEMPLAR_WS ?? '/Users/jeremy/projects/media-server/docs/backlog/shows/comps/r3/workshop'
const IMG = process.env.EXEMPLAR_IMG ?? `${WS}/img`
const OUT = process.env.CAPTURE_OUT ?? 'capture-out'

const SHIGATSU: Array<[string, number]> = [
  ['Monotone/Colorful', 64], ['Friend A', 59], ['Inside Spring', 56], ['Set Out', 65], ['Gray Skies', 66],
  ['On The Way Home', 60], ['The Shadows Whisper', 47], ['Let It Ring', 50], ['Resonance', 61],
  ['The Scenery I Shared With You', 58], ['Light of Life', 48], ['Twinkle Little Star', 54], ["Love's Sadness", 53],
  ['Footprints', 67], ['Liar', 46], ['Two of a Kind', 63], ['Twilight', 49], ['When Hearts Come Together', 55],
  ['Goodbye, Hero', 52], ['Hand in Hand', 51], ['Snow', 57], ['Spring Breeze', 62],
]

interface ExemplarShow {
  key: string
  title: string
  episodes: { item_id: number; season: number; episode: number; title: string; duration_s: number; thumb: string }[]
  missing: { season: number; number: number; title: string; airdate: string | null }[]
}
const sandbox: { window: { DATA?: { shows: ExemplarShow[]; synthetic_shows: ExemplarShow[] } } } = { window: {} }
runInNewContext(readFileSync(`${WS}/data.js`, 'utf8'), sandbox)
const EXEMPLAR = [...sandbox.window.DATA!.shows, ...sandbox.window.DATA!.synthetic_shows]

/** An exemplar show as player mock data: every library episode playable. */
function exemplarShow(key: string, id: number, thumbs: Record<number, string>, items: Record<number, ItemEntry>) {
  const s = EXEMPLAR.find((x) => x.key === key)!
  const show = { id, title: s.title }
  const bySeason = new Map<number, (ShowEpisodeEntry & { n: number })[]>()
  const push = (season: number, entry: ShowEpisodeEntry, n: number) =>
    bySeason.set(season, [...(bySeason.get(season) ?? []), { ...entry, n } as ShowEpisodeEntry & { n: number }])
  for (const e of s.episodes) {
    const item = episode(e.item_id, e.season, e.episode, e.title, { duration_s: e.duration_s, show })
    items[e.item_id] = item
    thumbs[e.item_id] = `${WS}/${e.thumb}`
    push(e.season, item, e.episode)
  }
  for (const m of s.missing) push(m.season, missingEpisode(m.season, m.number, m.title, m.airdate ?? '2020-01-01'), m.number)
  const seasons: ShowSeason[] = [...bySeason.entries()]
    .sort(([a], [b]) => (a === 0 ? 1 : b === 0 ? -1 : a - b))
    .map(([season, list]) => ({ season, episodes: list.sort((a, b) => a.n - b.n).map(({ n: _n, ...e }) => (void _n, e as ShowEpisodeEntry)) }))
  return showDetail(showEntry(id, { title: s.title, season_count: seasons.filter((x) => x.season > 0).length }), { seasons })
}

function captureMock(): { mock: PlayerMock; thumbs: Record<number, string> } {
  const thumbs: Record<number, string> = {}
  const shigatsu = SHIGATSU.map(([title, thumb], i) => {
    thumbs[1001 + i] = `${IMG}/thumbs/${thumb}.jpg`
    return episode(1001 + i, 1, i + 1, title, { duration_s: 1420, show: { id: 5, title: 'Shigatsu wa Kimi no Uso' } })
  })
  const furiousShow = { id: 1, title: 'Furious' }
  const furious = [
    episode(103, 1, 3, 'Memory Distortion', { show: furiousShow }),
    episode(105, 1, 5, 'Pick a Sticker', { show: furiousShow }),
  ]
  thumbs[105] = `${IMG}/thumbs/119.jpg`
  const silo = episode(302, 3, 2, 'The Engineer', { show: { id: 3, title: 'Silo' } })
  const items: Record<number, ItemEntry> = Object.fromEntries([...shigatsu, ...furious, silo].map((e) => [e.id, e]))
  const euphoria = exemplarShow('euphoria', 7, thumbs, items)
  const greys = exemplarShow('greys', 8, thumbs, items)
  return {
    thumbs,
    mock: {
      items,
      shows: {
        7: euphoria,
        8: greys,
        5: showDetail(showEntry(5, { title: 'Shigatsu wa Kimi no Uso' }), { seasons: [{ season: 1, episodes: shigatsu }] }),
        1: showDetail(showEntry(1, { title: 'Furious' }), { seasons: [{ season: 1, episodes: furious }] }),
        3: showDetail(showEntry(3, { title: 'Silo' }), { seasons: [{ season: 3, episodes: [silo] }] }),
      },
      markers: { 103: { credits_start_s: 30 }, 302: { intro: { start_s: 1, end_s: 35 } } },
    },
  }
}

async function openPlayer(browser: Browser, viewport: { width: number; height: number }, frame: string, path: string) {
  const touch = viewport.width < 640
  const context = await browser.newContext({ viewport, deviceScaleFactor: 2, colorScheme: 'dark', isMobile: touch, hasTouch: touch })
  const page = await context.newPage()
  const { mock, thumbs } = captureMock()
  await mockPlayerApi(page, mock, playerFixture(`${IMG}/frames/${frame}`))
  // Registered after the API mock, so it wins for the thumbnails it knows.
  await page.route(
    (url) => /^\/api\/items\/\d+\/thumb$/.test(url.pathname),
    async (route) => {
      const id = Number(new URL(route.request().url()).pathname.split('/')[3])
      const file = thumbs[id]
      if (!file) return route.fallback()
      await route.fulfill({ status: 200, contentType: 'image/jpeg', body: readFileSync(file) })
    },
  )
  await page.goto(path)
  await page.locator('video').evaluate(
    (el: HTMLVideoElement) => new Promise((resolve) => (el.currentTime > 0 ? resolve(null) : el.addEventListener('playing', resolve, { once: true }))),
  )
  return { context, page }
}

async function pauseAt(page: Page, seconds: number) {
  await page.locator('video').evaluate((el: HTMLVideoElement, t) => {
    el.pause()
    el.currentTime = t
  }, seconds)
}

const FRAMES = [['phone', { width: 390, height: 844 }], ['desktop', { width: 1440, height: 900 }]] as const
const POPOVERS = [
  ['b', 'shigatsu-e20-mid.jpg', '/watch/1020'],
  ['b-seasons', 'euphoria-s2e8-mid.jpg', '/watch/40'],
  ['b-23', '../synthetic/greys/s21e7.jpg', '/watch/900007'],
] as const

for (const [name, viewport] of FRAMES) {
  for (const [variant, frame, path] of POPOVERS) {
    test(`episodes popover ${variant} ${name}`, async ({ browser }) => {
      const { context, page } = await openPlayer(browser, viewport, frame, path)
      await pauseAt(page, 9)
      await page.getByRole('button', { name: 'Episodes' }).click()
      await page.getByRole('dialog', { name: 'Episodes' }).locator('img').first().waitFor()
      await page.waitForTimeout(600)
      await page.screenshot({ path: `${OUT}/player-pop-${variant}-${name}.png` })
      await context.close()
    })
  }

  test(`control bar ${name}`, async ({ browser }) => {
    const { context, page } = await openPlayer(browser, viewport, 'shigatsu-e20-mid.jpg', '/watch/1020')
    await pauseAt(page, 9)
    await page.mouse.move(viewport.width / 2, viewport.height / 2)
    await page.waitForTimeout(400)
    await page.screenshot({ path: `${OUT}/player-bar-${name}.png` })
    await context.close()
  })

  test(`next card ${name}`, async ({ browser }) => {
    const { context, page } = await openPlayer(browser, viewport, 'furious-s1e3-end.jpg', '/watch/103')
    await page.locator('video').evaluate((el: HTMLVideoElement) => (el.currentTime = 31))
    await page.locator('[data-next-card] img').waitFor()
    await page.waitForTimeout(3600)
    await page.screenshot({ path: `${OUT}/player-next-${name}.png` })
    await context.close()
  })

  test(`skip intro ${name}`, async ({ browser }) => {
    const { context, page } = await openPlayer(browser, viewport, 'silo-s3e2-intro.jpg', '/watch/302')
    await page.locator('video').evaluate((el: HTMLVideoElement) => (el.currentTime = 3))
    await page.getByRole('button', { name: 'Skip intro' }).waitFor()
    await page.waitForTimeout(3600)
    await page.screenshot({ path: `${OUT}/player-skip-${name}.png` })
    await context.close()
  })
}
