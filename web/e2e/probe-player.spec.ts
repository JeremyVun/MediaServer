import { expect, test, type Page } from '@playwright/test'
import { furiousMock, mockPlayerApi, trackFirstPlaying, type PlayerLog } from './support/player'

test.skip(({ browserName }) => browserName !== 'chromium', 'Chromium plays the VP9 fixture')
test.describe.configure({ timeout: 60_000 })

const DESKTOP = { width: 1440, height: 900 }
const video = (page: Page) => page.locator('video')
const nextCard = (page: Page) => page.locator('[data-next-card]')

async function seek(page: Page, seconds: number) {
  await video(page).evaluate((el: HTMLVideoElement, t) => {
    el.currentTime = t
  }, seconds)
}

async function waitPlaying(page: Page) {
  await expect.poll(() => video(page).evaluate((el: HTMLVideoElement) => !el.paused && el.currentTime > 0)).toBe(true)
}

async function waitPast(page: Page, seconds: number) {
  await expect.poll(() => video(page).evaluate((el: HTMLVideoElement) => el.currentTime)).toBeGreaterThan(seconds)
}

async function wakeControls(page: Page) {
  await page.mouse.move(600, 400)
  await page.mouse.move(640, 420)
}

async function openFromLibrary(page: Page) {
  await page.goto('/')
  await page.getByRole('link', { name: 'Play Furious S1 E3' }).click()
  await expect(page).toHaveURL(/\/watch\/103$/)
  await waitPlaying(page)
}

function savesFor(log: PlayerLog, itemID: number) {
  return log.progress.filter((p) => p.itemID === itemID)
}

function savesAfterFirstCompleted(log: PlayerLog, itemID: number) {
  const saves = savesFor(log, itemID)
  const first = saves.findIndex((p) => p.body.completed === true)
  expect(first, `a completed save for ${itemID}`).toBeGreaterThanOrEqual(0)
  return saves.slice(first)
}

// probe: once the player sends completed for the episode it advanced from, no later save for it drops the flag
test('probe: Progress countdown advance never undoes completed', async ({ page }) => {
  await page.setViewportSize(DESKTOP)
  const log = await mockPlayerApi(page, furiousMock())
  await openFromLibrary(page)
  await seek(page, 30.5)
  await expect(nextCard(page)).toBeVisible()
  await expect(page).toHaveURL(/\/watch\/105$/, { timeout: 15_000 })
  await waitPlaying(page)
  await page.waitForTimeout(1_500)
  const after = savesAfterFirstCompleted(log, 103)
  expect(after.map((p) => p.body.completed)).toEqual(after.map(() => true))
})

// probe: the advance still happens, and still counts as watched, when the completed write fails
test('probe: Progress advance with a failing completed write keeps completed on later saves', async ({ page }) => {
  await page.setViewportSize(DESKTOP)
  let failed = false
  const log = await mockPlayerApi(page, furiousMock())
  // Registered after the mock, so it sees the request first.
  await page.route(
    (url) => url.pathname === '/api/items/103/progress',
    async (route) => {
      const body = route.request().postDataJSON() as { completed?: boolean }
      if (!failed && body.completed === true) {
        failed = true
        await route.fulfill({ status: 500, contentType: 'application/json', body: '{"error":{"code":"internal","message":"x"}}' })
        return
      }
      await route.fallback()
    },
  )
  await openFromLibrary(page)
  await seek(page, 31)
  await nextCard(page).click()
  await expect(page).toHaveURL(/\/watch\/105$/)
  await waitPlaying(page)
  await page.waitForTimeout(1_500)
  expect(failed).toBe(true)
  const later = savesFor(log, 103)
  expect(later.length, 'the switch flushes the advanced-from episode again').toBeGreaterThan(0)
  expect(later.every((p) => p.body.completed === true)).toBe(true)
})

// probe: switching episodes from the popover saves the old episode's own position and none of it to the new one
test('probe: Progress popover switch keeps each item’s position on its own item', async ({ page }) => {
  await page.setViewportSize(DESKTOP)
  const log = await mockPlayerApi(page, furiousMock())
  await openFromLibrary(page)
  await seek(page, 24)
  await waitPast(page, 25)
  await wakeControls(page)
  await page.getByRole('button', { name: 'Episodes' }).click()
  const popover = page.getByRole('dialog', { name: 'Episodes' })
  await popover.getByRole('button', { name: /Pick a Sticker/ }).click()
  await expect(page).toHaveURL(/\/watch\/105$/)
  await waitPlaying(page)
  await page.waitForTimeout(2_500)

  const old = savesFor(log, 103)
  expect(old.length).toBeGreaterThan(0)
  expect(old.at(-1)!.body.position_s, 'the old episode’s last save is its own last position').toBeGreaterThanOrEqual(24)
  const fresh = savesFor(log, 105)
  expect(fresh.filter((p) => p.body.position_s >= 20)).toEqual([])
  expect(fresh.filter((p) => p.body.completed === true)).toEqual([])
})

// probe: two quick switches never credit a position to an item the viewer didn't watch it on
test('probe: Progress rapid double switch credits nothing to the skipped episode', async ({ page }) => {
  await page.setViewportSize(DESKTOP)
  const log = await mockPlayerApi(page, furiousMock())
  await page.route(
    (url) => url.pathname === '/api/items/105/play',
    async (route) => {
      await new Promise((resolve) => setTimeout(resolve, 1_500))
      await route.fallback()
    },
  )
  await openFromLibrary(page)
  await seek(page, 24)
  await waitPast(page, 25)
  const trigger = page.getByRole('button', { name: 'Episodes' })
  const popover = page.getByRole('dialog', { name: 'Episodes' })
  await wakeControls(page)
  await trigger.click()
  await popover.getByRole('button', { name: /Pick a Sticker/ }).click()
  await expect(page).toHaveURL(/\/watch\/105$/)
  await expect(trigger).toBeVisible()
  await wakeControls(page)
  await trigger.click()
  await popover.getByRole('button', { name: /Redline/ }).click()
  await expect(page).toHaveURL(/\/watch\/106$/)
  await waitPlaying(page)
  await page.waitForTimeout(2_000)

  expect(savesFor(log, 105).filter((p) => p.body.position_s > 1)).toEqual([])
  expect(savesFor(log, 106).filter((p) => p.body.position_s >= 20)).toEqual([])
  expect(savesFor(log, 103).at(-1)!.body.position_s).toBeGreaterThanOrEqual(24)
})

// probe: picking an episode closes the Episodes popover and it stays closed on the new episode
test('probe: Episodes popover stays closed after an episode pick', async ({ page }) => {
  await page.setViewportSize(DESKTOP)
  await mockPlayerApi(page, furiousMock())
  await openFromLibrary(page)
  await wakeControls(page)
  await page.getByRole('button', { name: 'Episodes' }).click()
  const popover = page.getByRole('dialog', { name: 'Episodes' })
  await popover.getByRole('button', { name: /Pick a Sticker/ }).click()
  await expect(page).toHaveURL(/\/watch\/105$/)
  await waitPlaying(page)
  await page.waitForTimeout(500)
  await expect(popover).toBeHidden()
})

type Seen = { api: string[]; modules: string[] }

/** Requests up to the first `playing` event, plus `graceMs` after it. */
async function seenAroundFirstFrame(page: Page, path: string, graceMs: number): Promise<Seen> {
  const firstPlaying = await trackFirstPlaying(page)
  const all: { url: URL; method: string; at: number }[] = []
  page.on('request', (request) => all.push({ url: new URL(request.url()), method: request.method(), at: Date.now() }))
  await mockPlayerApi(page, furiousMock())
  await page.goto(path)
  await waitPlaying(page)
  await page.waitForTimeout(graceMs)
  const at = firstPlaying()
  expect(at).not.toBeNull()
  const before = all.filter((r) => r.at <= at! + graceMs)
  const norm = (s: string) => s.replace(/\/\d+(?=\/|$)/g, '/:id')
  return {
    api: [...new Set(before.filter((r) => r.url.pathname.startsWith('/api/')).map((r) => `${r.method} ${norm(r.url.pathname)}`))].sort(),
    modules: [...new Set(before.filter((r) => r.url.pathname.startsWith('/src/')).map((r) => r.url.pathname))].sort(),
  }
}

// probe: an episode makes no request before its first frame that a movie doesn't make
test('probe: Lean player episode requests match a movie before the first frame', async ({ browser }) => {
  const moviePage = await browser.newPage({ viewport: DESKTOP })
  const movie = await seenAroundFirstFrame(moviePage, '/watch/900', 2_000)
  await moviePage.close()
  const episodePage = await browser.newPage({ viewport: DESKTOP })
  const episode = await seenAroundFirstFrame(episodePage, '/watch/103', 0)
  await episodePage.close()
  expect(episode.api.filter((line) => !movie.api.includes(line))).toEqual([])
  expect(episode.modules.filter((m) => !movie.modules.includes(m))).toEqual([])
})

function domShape(page: Page) {
  return page.locator('main').evaluate((main) => {
    const out: string[] = []
    const walk = (el: Element, depth: number) => {
      if (el.closest('svg') && el.tagName.toLowerCase() !== 'svg') return
      const label = el.getAttribute('aria-label') ?? ''
      const data = [...el.attributes].filter((a) => a.name.startsWith('data-')).map((a) => a.name).join(',')
      out.push(`${depth}:${el.tagName.toLowerCase()}[${label}][${data}]`)
      // Buffered ranges inside the seek bar come and go with seeks.
      if (el.hasAttribute('data-seek')) return
      for (const child of el.children) walk(child, depth + 1)
    }
    walk(main, 0)
    return out
  })
}

// probe: a movie with intro and credits markers renders the same DOM at the intro, past halfway, at the credits and at the end
test('probe: Lean player movie DOM never changes shape through its markers', async ({ page }) => {
  await page.setViewportSize(DESKTOP)
  const modules: string[] = []
  page.on('request', (request) => modules.push(new URL(request.url()).pathname))
  const log = await mockPlayerApi(page, furiousMock())
  await page.goto('/watch/900')
  await waitPlaying(page)
  await video(page).evaluate((el: HTMLVideoElement) => el.pause())
  await seek(page, 3)
  await page.waitForTimeout(400)
  const atIntro = await domShape(page)
  for (const t of [21, 30.5]) {
    await seek(page, t)
    await page.waitForTimeout(400)
    expect(await domShape(page), `DOM at ${t}s`).toEqual(atIntro)
  }
  await video(page).evaluate((el: HTMLVideoElement) => {
    el.currentTime = 39
    void el.play()
  })
  await expect.poll(() => video(page).evaluate((el: HTMLVideoElement) => el.ended), { timeout: 10_000 }).toBe(true)
  await page.waitForTimeout(600)
  expect(await domShape(page), 'DOM at the end').toEqual(atIntro)
  expect(modules.filter((m) => /EpisodesPopover|NextEpisodeCard/.test(m))).toEqual([])
  expect(log.api.filter((r) => r.line.includes('/api/shows'))).toEqual([])
})

// probe: with a slow session start (a cold HLS transcode), nothing from the shows feature downloads before the first frame
test('probe: Lean player slow first frame loads no shows code first', async ({ page }) => {
  await page.setViewportSize(DESKTOP)
  const firstPlaying = await trackFirstPlaying(page)
  const modules: { path: string; at: number }[] = []
  page.on('request', (request) => modules.push({ path: new URL(request.url()).pathname, at: Date.now() }))
  await mockPlayerApi(page, furiousMock())
  await page.route(
    (url) => url.pathname === '/api/items/900/play',
    async (route) => {
      await new Promise((resolve) => setTimeout(resolve, 3_000))
      await route.fallback()
    },
  )
  await page.goto('/watch/900')
  await waitPlaying(page)
  const at = firstPlaying()!
  const early = modules.filter((m) => m.at <= at && /features\/show\/|EpisodesPopover|NextEpisodeCard/.test(m.path))
  expect(early.map((m) => m.path)).toEqual([])
})
