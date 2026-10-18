import { expect, test, type Page } from '@playwright/test'
import { itemEntry, mockLibraryApi, showEntry, upNext, type LibraryMock } from './support/library.ts'

const freshShow = showEntry(1, { title: 'Masterplan', collection_ids: [7] })
const startedShow = showEntry(2, {
  title: 'Dark Matter',
  has_progress: true,
  needs_check: true,
  up_next: upNext(202, { season: 2, episode: 2, progress: { position_s: 600, completed: false } }),
})
const freshMovie = itemEntry(10, { title: 'Alien', collection_ids: [7] })
const halfMovie = itemEntry(11, { title: 'Zodiac', progress: { position_s: 3000, completed: false }, collection_ids: [7] })
const doneMovie = itemEntry(12, { title: 'Brazil', progress: { position_s: 6000, completed: true } })
const zeroVideo = itemEntry(13, { type: 'video', title: 'Home video', progress: { position_s: 0, completed: false } })
const pendingEpisode = itemEntry(14, { type: 'episode', title: 'Andor S01E01', needs_check: true })

function fixture(entries = [freshShow, startedShow, freshMovie, halfMovie, doneMovie, zeroVideo, pendingEpisode]): LibraryMock {
  return {
    entries,
    continueWatching: [startedShow, halfMovie],
    collections: [{ id: 7, name: 'Watchlist', item_count: 3, thumb_urls: [] }],
    shows: {},
  }
}

const grid = (page: Page) => page.locator('[data-library-grid]')
const continueRow = (page: Page) => page.locator('[data-continue-watching]')
const toggle = (page: Page) => page.getByRole('checkbox', { name: 'Unwatched' })
const toggleLabel = (page: Page) => page.locator('label', { has: toggle(page) })
const gridTitles = (page: Page) =>
  grid(page)
    .locator(':scope > *')
    .evaluateAll((cards) => cards.map((card) => card.querySelector('[aria-label]')?.getAttribute('aria-label') ?? ''))

test('Unwatched narrows the grid to titles not started, composing with tabs and collections', async ({ page }) => {
  const log = await mockLibraryApi(page, fixture())
  await page.goto('/')
  await expect(grid(page).getByRole('link', { name: 'Play Zodiac' })).toBeVisible()
  const rowLinks = () =>
    continueRow(page)
      .getByRole('link')
      .evaluateAll((links) => links.map((link) => `${link.getAttribute('aria-label')} ${link.getAttribute('href')}`))
  const before = await rowLinks()
  expect(before.length).toBeGreaterThan(0)

  await expect(toggle(page)).not.toBeChecked()
  await toggle(page).click()
  await expect(toggle(page)).toBeChecked()
  await expect(page).toHaveURL(/[?&]unwatched=1(&|$)/)
  await expect
    .poll(() => gridTitles(page))
    .toEqual(['Open Masterplan', 'Play Alien', 'Play Home video', 'Play Andor S01E01'])
  expect(log.libraryRequests.some((p) => p.get('unwatched') === '1' && p.get('in_progress') == null)).toBe(true)
  expect(log.libraryRequests.every((p) => p.get('in_progress') !== '1' || p.get('unwatched') == null)).toBe(true)
  await expect.poll(rowLinks).toEqual(before)

  const tabs = page.getByRole('tablist', { name: 'Show' })
  await tabs.getByRole('tab', { name: 'Shows' }).click()
  await expect(page).toHaveURL(/type=show/)
  await expect(page).toHaveURL(/unwatched=1/)
  await expect.poll(() => gridTitles(page)).toEqual(['Open Masterplan'])
  expect(log.libraryRequests.some((p) => p.get('unwatched') === '1' && p.get('type') === 'show')).toBe(true)
  await expect.poll(rowLinks).toEqual(before)

  await tabs.getByRole('tab', { name: 'Movies' }).click()
  await expect.poll(() => gridTitles(page)).toEqual(['Play Alien'])
  await expect(toggle(page)).toBeChecked()

  await tabs.getByRole('tab', { name: 'All' }).click()
  await page.getByRole('button', { name: 'Watchlist' }).click()
  await expect(page).toHaveURL(/collection=7/)
  await expect(page).toHaveURL(/unwatched=1/)
  await expect.poll(() => gridTitles(page)).toEqual(['Open Masterplan', 'Play Alien'])
  expect(log.libraryRequests.some((p) => p.get('unwatched') === '1' && p.get('collection_id') === '7')).toBe(true)

  await toggle(page).click()
  await expect(page).not.toHaveURL(/unwatched/)
  await expect(page).toHaveURL(/collection=7/)
  await expect.poll(() => gridTitles(page)).toEqual(['Open Masterplan', 'Play Alien', 'Play Zodiac'])
})

test('a deep link restores the toggle, and the toggle stays neutral', async ({ page }) => {
  const log = await mockLibraryApi(page, fixture())
  await page.goto('/?type=movie&unwatched=1')
  await expect(toggle(page)).toBeChecked()
  await expect.poll(() => gridTitles(page)).toEqual(['Play Alien'])
  expect(log.libraryRequests.some((p) => p.get('unwatched') === '1' && p.get('type') === 'movie')).toBe(true)

  const fill = (selector: ReturnType<Page['locator']>) =>
    selector.evaluate((el) => getComputedStyle(el.querySelector('span') ?? el).backgroundColor)
  const amber = await page.getByRole('button', { name: 'Upload', exact: true }).evaluate((el) => getComputedStyle(el).backgroundColor)
  expect(await fill(toggle(page))).not.toBe(amber)
})

test('B18: Unwatched is a checkbox whose label is a 44 px target with a check mark when checked', async ({ page }) => {
  await mockLibraryApi(page, fixture())
  await page.goto('/')
  const box = await toggleLabel(page).boundingBox()
  expect(box!.height).toBeGreaterThanOrEqual(44)
  const mark = toggleLabel(page).locator('svg')
  await expect(mark).toHaveCSS('opacity', '0')
  await toggleLabel(page).getByText('Unwatched').click()
  await expect(toggle(page)).toBeChecked()
  await expect(page).toHaveURL(/[?&]unwatched=1(&|$)/)
  await expect(mark).toHaveCSS('opacity', '1')
  await toggle(page).focus()
  await page.keyboard.press('Space')
  await expect(toggle(page)).not.toBeChecked()
  await expect(page).not.toHaveURL(/unwatched/)
})

test('nothing left unstarted reads plainly, and the toggle stays to turn it off', async ({ page }) => {
  await mockLibraryApi(page, fixture([startedShow, halfMovie, doneMovie]))
  await page.goto('/?unwatched=1')
  await expect(page.getByText("You've started everything here")).toBeVisible()
  await expect(grid(page)).toHaveCount(0)
  await expect(continueRow(page)).toBeVisible()
  await toggle(page).click()
  await expect(page).not.toHaveURL(/unwatched/)
  await expect(grid(page).getByRole('link', { name: 'Play Zodiac' })).toBeVisible()
})

test('B15: flagged cards carry no Check badge in the grid or Continue watching', async ({ page }) => {
  await mockLibraryApi(page, fixture())
  await page.goto('/')
  await expect(grid(page).getByRole('link', { name: 'Play Andor S01E01' })).toBeVisible()
  await expect(grid(page).getByText('Check', { exact: true })).toHaveCount(0)
  await expect(continueRow(page).getByRole('link', { name: 'Open Dark Matter' })).toBeVisible()
  await expect(continueRow(page).getByText('Check', { exact: true })).toHaveCount(0)
})

test.describe('on a phone', () => {
  test.use({ viewport: { width: 390, height: 844 } })

  for (const url of ['/', '/?unwatched=1&type=movie', '/?q=a&unwatched=1']) {
    test(`the toggle shares the tabs row without wrapping (${url})`, async ({ page }) => {
      await mockLibraryApi(page, fixture())
      await page.goto(url)
      await expect(grid(page)).toBeVisible()
      const tabBox = await page.getByRole('tab', { name: 'All' }).boundingBox()
      const toggleBox = await toggleLabel(page).boundingBox()
      const selectBox = await page.getByRole('button', { name: 'Select', exact: true }).boundingBox()
      expect(toggleBox!.y).toBe(tabBox!.y)
      expect(selectBox!.y + selectBox!.height / 2).toBeCloseTo(tabBox!.y + tabBox!.height / 2, 0)
      expect(toggleBox!.x + toggleBox!.width).toBeLessThanOrEqual(selectBox!.x)
      expect(selectBox!.x + selectBox!.width).toBeLessThanOrEqual(390)
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390)
    })
  }
})
