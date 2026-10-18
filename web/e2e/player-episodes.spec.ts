import { expect, test, type Page } from '@playwright/test'
import { episode, furiousMock, mockPlayerApi, trackFirstPlaying, type PlayerLog } from './support/player'

test.skip(({ browserName }) => browserName !== 'chromium', 'Chromium plays the VP9 fixture; one engine is enough here')
test.describe.configure({ timeout: 60_000 })

const DESKTOP = { width: 1440, height: 900 }
const PHONE = { width: 390, height: 844 }

const video = (page: Page) => page.locator('video')
const nextCard = (page: Page) => page.locator('[data-next-card]')

async function seek(page: Page, seconds: number) {
  await video(page).evaluate((el: HTMLVideoElement, t) => {
    el.currentTime = t
  }, seconds)
}

async function currentTime(page: Page) {
  return video(page).evaluate((el: HTMLVideoElement) => el.currentTime)
}

async function waitPlaying(page: Page) {
  await expect.poll(() => video(page).evaluate((el: HTMLVideoElement) => !el.paused && el.currentTime > 0)).toBe(true)
}

/** Starts at the library and plays Furious S1 E3 from its show card, so the
    player has a page behind it in history. */
async function openFromLibrary(page: Page) {
  await page.goto('/')
  await page.getByRole('link', { name: 'Play Furious S1 E3' }).click()
  await expect(page).toHaveURL(/\/watch\/103$/)
  await waitPlaying(page)
}

function requestsBefore(log: PlayerLog, at: number | null) {
  expect(at, 'a playing event fired').not.toBeNull()
  return log.api.filter((request) => request.at <= (at ?? 0)).map((request) => request.line)
}

test('nothing new is requested before the first frame', async ({ page }) => {
  await page.setViewportSize(DESKTOP)
  const firstPlaying = await trackFirstPlaying(page)
  const log = await mockPlayerApi(page, furiousMock())
  const modules: { url: string; at: number }[] = []
  page.on('request', (request) => modules.push({ url: request.url(), at: Date.now() }))
  await openFromLibrary(page)

  const before = requestsBefore(log, firstPlaying())
  expect(before.filter((line) => /^GET \/api\/shows\/\d+$/.test(line))).toEqual([])
  expect(before).toContain('POST /api/items/103/play')
  const lazy = modules.filter((m) => m.at <= (firstPlaying() ?? 0) && /EpisodesPopover|NextEpisodeCard|SeasonChips/.test(m.url))
  expect(lazy).toEqual([])
})

test('the card appears at the credits marker and the countdown advances, replacing history', async ({ page }) => {
  await page.setViewportSize(DESKTOP)
  const log = await mockPlayerApi(page, furiousMock())
  await openFromLibrary(page)

  await seek(page, 29)
  await page.waitForTimeout(600)
  await expect(nextCard(page)).toHaveCount(0)
  await seek(page, 30.5)
  await expect(nextCard(page)).toBeVisible()
  // E4 is missing, so the next available episode is E5.
  await expect(nextCard(page)).toContainText('S1 E5 · Pick a Sticker')
  await expect(nextCard(page).locator('[data-countdown]')).toBeVisible()
  await expect(nextCard(page).getByRole('button')).toHaveCount(0)
  await expect(nextCard(page)).not.toContainText(/credits|isn't in your library/i)

  await expect(page).toHaveURL(/\/watch\/105$/, { timeout: 15_000 })
  const completed = log.progress.find((p) => p.itemID === 103 && p.body.completed === true)
  expect(completed, 'completed: true sent for the finished episode').toBeTruthy()
  const nextPlay = log.plays.find((p) => p.itemID === 105)
  expect(nextPlay).toBeTruthy()
  expect(completed!.at).toBeLessThanOrEqual(nextPlay!.at)
  expect(log.progress.filter((p) => p.itemID === 105 && p.body.position_s > 20)).toEqual([])
  await waitPlaying(page)
  await expect(page.getByRole('button', { name: /Furious · S1 E5 · Pick a Sticker/ })).toBeVisible()

  await page.goBack()
  await expect(page).toHaveURL(/\/$/)
})

test('tapping the card plays the next episode now', async ({ page }) => {
  await page.setViewportSize(DESKTOP)
  const log = await mockPlayerApi(page, furiousMock())
  await openFromLibrary(page)
  await seek(page, 31)
  await nextCard(page).click()
  await expect(page).toHaveURL(/\/watch\/105$/)
  expect(log.progress.some((p) => p.itemID === 103 && p.body.completed === true)).toBe(true)
})

// D2: the server numbers an episode without season or episode S1 E0, so the
// client must too, or autoplay stops at a video moved into a show.
test('a numberless episode is S1 E0, so the card offers S1 E1', async ({ page }) => {
  await page.setViewportSize(DESKTOP)
  const mock = furiousMock()
  const moved = episode(150, 1, 1, 'Moved in', { season: null, episode: null })
  mock.items[150] = moved
  mock.shows[1].seasons[0].episodes.unshift(moved)
  await mockPlayerApi(page, mock)
  await page.goto('/watch/150')
  await waitPlaying(page)
  await seek(page, 20.5)
  await expect(nextCard(page)).toContainText('S1 E1 · Pilot')
})

test('with no marker the card appears 20 s before the end; tap outside dismisses and cancels', async ({ page }) => {
  await page.setViewportSize(DESKTOP)
  const log = await mockPlayerApi(page, furiousMock())
  await page.goto('/watch/105')
  await waitPlaying(page)

  await seek(page, 19)
  await page.waitForTimeout(600)
  await expect(nextCard(page)).toHaveCount(0)
  await seek(page, 20.5)
  await expect(nextCard(page)).toContainText('S1 E6 · Redline')

  await page.mouse.click(300, 300)
  await expect(nextCard(page)).toHaveCount(0)
  // The dismissing tap does nothing else: playback carries on.
  await expect.poll(() => video(page).evaluate((el: HTMLVideoElement) => el.paused)).toBe(false)
  await page.waitForTimeout(11_000)
  await expect(page).toHaveURL(/\/watch\/105$/)
  expect(log.progress.some((p) => p.body.completed === true)).toBe(false)
})

test('a key or Esc dismisses the card without leaving the player', async ({ page }) => {
  await page.setViewportSize(DESKTOP)
  await mockPlayerApi(page, furiousMock())
  await page.goto('/watch/105')
  await waitPlaying(page)

  await seek(page, 21)
  await expect(nextCard(page)).toBeVisible()
  await page.keyboard.press('k')
  await expect(nextCard(page)).toHaveCount(0)

  // Back before the card's start re-arms it.
  await seek(page, 5)
  await expect.poll(() => currentTime(page)).toBeLessThan(10)
  await page.waitForTimeout(400)
  await seek(page, 21)
  await expect(nextCard(page)).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(nextCard(page)).toHaveCount(0)
  await page.waitForTimeout(300)
  await expect(page).toHaveURL(/\/watch\/105$/)
})

test('after dismissal, ended shows the card again without a countdown', async ({ page }) => {
  await page.setViewportSize(DESKTOP)
  await mockPlayerApi(page, furiousMock())
  await page.goto('/watch/105')
  await waitPlaying(page)
  await seek(page, 21)
  await expect(nextCard(page)).toBeVisible()
  await page.keyboard.press('k')
  await expect(nextCard(page)).toHaveCount(0)

  await seek(page, 39.5)
  await expect(nextCard(page)).toBeVisible({ timeout: 5_000 })
  await expect(nextCard(page).locator('[data-countdown]')).toHaveCount(0)
  await page.waitForTimeout(11_000)
  await expect(page).toHaveURL(/\/watch\/105$/)
})

test('Skip intro shows only inside the intro and seeks to its end', async ({ page }) => {
  await page.setViewportSize(DESKTOP)
  await mockPlayerApi(page, furiousMock())
  await page.goto('/watch/103')
  await waitPlaying(page)
  await video(page).evaluate((el: HTMLVideoElement) => el.pause())

  await seek(page, 1)
  await expect(page.getByRole('button', { name: 'Skip intro' })).toHaveCount(0)
  await seek(page, 3)
  const skip = page.getByRole('button', { name: 'Skip intro' })
  await expect(skip).toBeVisible()
  const box = await skip.boundingBox()
  expect(box!.x).toBeLessThan(DESKTOP.width / 2)
  expect(box!.y).toBeGreaterThan(DESKTOP.height / 2)
  const look = await skip.evaluate((el) => {
    const style = getComputedStyle(el)
    return { background: style.backgroundColor, border: style.borderTopWidth }
  })
  expect(look).toEqual({ background: 'rgba(0, 0, 0, 0)', border: '0px' })

  await skip.click()
  await expect.poll(() => currentTime(page)).toBeGreaterThanOrEqual(8)
  await expect(skip).toHaveCount(0)
})

test('desktop Episodes popover: clear of the seek bar, video box unchanged, switches in place', async ({ page }) => {
  await page.setViewportSize(DESKTOP)
  const log = await mockPlayerApi(page, furiousMock())
  await openFromLibrary(page)
  await video(page).evaluate((el: HTMLVideoElement) => el.pause())
  expect(log.api.some((r) => r.line === 'GET /api/shows/1')).toBe(false)

  const videoBefore = await video(page).boundingBox()
  const trigger = page.getByRole('button', { name: 'Episodes' })
  await expect(trigger).toHaveText('Episodes')
  const order = await page
    .locator('[data-player-controls]')
    .last()
    .getByRole('button')
    .evaluateAll((buttons) => buttons.map((b) => b.getAttribute('aria-label') ?? b.textContent))
  expect(order.slice(-2)).toEqual(['Episodes', 'Fullscreen'])

  await trigger.click()
  const popover = page.getByRole('dialog', { name: 'Episodes' })
  await expect(popover).toBeVisible()
  expect(log.api.filter((r) => r.line === 'GET /api/shows/1')).toHaveLength(1)

  const playing = popover.locator('[aria-current="true"]')
  await expect(playing).toContainText('Burnout')
  await expect(playing).toContainText('Playing')
  await expect(popover.getByRole('tab', { name: 'Season 1' })).toHaveAttribute('aria-selected', 'true')
  await expect(popover.getByText('Gone')).toBeVisible()
  await expect(popover.getByRole('button', { name: /Gone/ })).toHaveCount(0)

  const pop = (await popover.boundingBox())!
  const seekBar = (await page.locator('[data-seek]').boundingBox())!
  const triggerBox = (await trigger.boundingBox())!
  expect(Math.abs(pop.y + pop.height + 8 - seekBar.y)).toBeLessThanOrEqual(1)
  expect(Math.abs(pop.x + pop.width - (triggerBox.x + triggerBox.width))).toBeLessThanOrEqual(1)
  expect(await video(page).boundingBox()).toEqual(videoBefore)

  const list = popover.locator('[id$="-list"]')
  const listBox = (await list.boundingBox())!
  const playingBox = (await playing.boundingBox())!
  expect(playingBox.y).toBeGreaterThanOrEqual(listBox.y - 1)
  expect(playingBox.y + playingBox.height).toBeLessThanOrEqual(listBox.y + listBox.height + 1)
  expect(listBox.height).toBeLessThanOrEqual(348)

  await popover.getByRole('tab', { name: 'Season 3, none in your library' }).click()
  await expect(popover.getByText('All 2 missing')).toBeVisible()
  await popover.getByRole('tab', { name: 'Season 1' }).click()

  await popover.getByRole('button', { name: /Pick a Sticker/ }).click()
  await expect(page).toHaveURL(/\/watch\/105$/)
  await expect(popover).toBeHidden()
  await waitPlaying(page)
  await expect(page.getByRole('button', { name: /Furious · S1 E5 · Pick a Sticker/ })).toBeVisible()
  expect(log.progress.filter((p) => p.itemID === 105 && p.body.position_s > 20)).toEqual([])

  await page.goBack()
  await expect(page).toHaveURL(/\/$/)
})

// D3: the popover's chunk is cached on a second visit, so the tap that
// mounts it must not also run the trigger's native toggle and shut it.
test('the Episodes popover opens on a second visit to the player, and stays shut after a pick', async ({ page }) => {
  await page.setViewportSize(DESKTOP)
  await mockPlayerApi(page, furiousMock())
  await openFromLibrary(page)
  const trigger = page.getByRole('button', { name: 'Episodes' })
  const popover = page.getByRole('dialog', { name: 'Episodes' })
  await trigger.click()
  await expect(popover).toBeVisible()
  await page.keyboard.press('Escape')
  await page.goBack()
  await page.getByRole('link', { name: 'Play Furious S1 E3' }).click()
  await waitPlaying(page)
  await page.mouse.move(700, 450)
  await trigger.click()
  await expect(popover).toBeVisible()
  await popover.getByRole('button', { name: /Redline/ }).click()
  await expect(page).toHaveURL(/\/watch\/106$/)
  await waitPlaying(page)
  await page.waitForTimeout(500)
  await expect(popover).toBeHidden()
})

test('phone Episodes popover opens below the top-bar icon, clear of the seek bar', async ({ page }) => {
  await page.setViewportSize(PHONE)
  await mockPlayerApi(page, furiousMock())
  await page.goto('/watch/103')
  await waitPlaying(page)
  await video(page).evaluate((el: HTMLVideoElement) => el.pause())

  const videoBefore = await video(page).boundingBox()
  const trigger = page.getByRole('button', { name: 'Episodes' })
  const triggerBox = (await trigger.boundingBox())!
  expect(triggerBox.width).toBeGreaterThanOrEqual(44)
  expect(triggerBox.height).toBeGreaterThanOrEqual(44)
  expect(triggerBox.y).toBeLessThan(60)

  await trigger.click()
  const popover = page.getByRole('dialog', { name: 'Episodes' })
  await expect(popover).toBeVisible()
  const pop = (await popover.boundingBox())!
  const seekBar = (await page.locator('[data-seek]').boundingBox())!
  expect(Math.abs(pop.y - (triggerBox.y + triggerBox.height + 4))).toBeLessThanOrEqual(1)
  expect(Math.abs(pop.x + pop.width - (triggerBox.x + triggerBox.width))).toBeLessThanOrEqual(1)
  expect(pop.y + pop.height).toBeLessThan(seekBar.y)
  expect(await video(page).boundingBox()).toEqual(videoBefore)

  const rows = popover.getByRole('button')
  for (const row of await rows.all()) expect((await row.boundingBox())!.height).toBeGreaterThanOrEqual(44)
  for (const tab of await popover.getByRole('tab').all()) expect((await tab.boundingBox())!.height).toBeGreaterThanOrEqual(44)

  await trigger.click()
  await expect(popover).toBeHidden()
})

test('a single-season show has no season chips in the popover', async ({ page }) => {
  await page.setViewportSize(DESKTOP)
  await mockPlayerApi(page, furiousMock())
  await page.goto('/watch/301')
  await waitPlaying(page)
  await page.getByRole('button', { name: 'Episodes' }).click()
  const popover = page.getByRole('dialog', { name: 'Episodes' })
  await expect(popover.locator('[aria-current="true"]')).toContainText('Only')
  await expect(popover.getByRole('tablist')).toHaveCount(0)
})

test('a movie gets no new player controls, even with markers', async ({ page }) => {
  await page.setViewportSize(DESKTOP)
  const log = await mockPlayerApi(page, furiousMock())
  await page.goto('/watch/900')
  await waitPlaying(page)
  await seek(page, 3)
  await video(page).evaluate((el: HTMLVideoElement) => el.pause())

  const controls = await page
    .locator('[data-player-controls]')
    .evaluateAll((bars) =>
      bars.map((bar) =>
        Array.from(bar.querySelectorAll('button, input'))
          .filter((el) => el.closest('[popover]') == null)
          .map((el) => el.getAttribute('aria-label') ?? el.textContent?.trim()),
      ),
    )
  // Exactly today's controls: the back link, then the control bar.
  expect(controls).toEqual([['Dune'], ['Seek', 'Play', 'Mute', 'Volume', 'Playback speed', 'Fullscreen']])
  await expect(page.getByRole('button', { name: 'Skip intro' })).toHaveCount(0)

  await seek(page, 35)
  await page.waitForTimeout(800)
  await expect(nextCard(page)).toHaveCount(0)
  expect(log.api.some((r) => r.line.startsWith('GET /api/shows/'))).toBe(false)
})
