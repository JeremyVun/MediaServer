import { expect, test, type Page } from '@playwright/test'
import { itemEntry, mockLibraryApi, showDetail, showEntry, upNext, type LibraryMock } from './support/library.ts'

const masterplan = showEntry(1, { title: 'Masterplan', episode_count: 6 })
const darkMatter = showEntry(2, {
  title: 'Dark Matter',
  has_progress: true,
  season_count: 2,
  up_next: upNext(202, { season: 2, episode: 2, progress: { position_s: 600, completed: false } }),
})
const monster = showEntry(3, {
  title: 'Monster',
  poster_url: null,
  backdrop_url: null,
  thumb_url: '/api/items/301/thumb?v=1',
  needs_check: true,
})
const invincible = showEntry(4, {
  title: 'Invincible',
  has_progress: true,
  up_next: upNext(405, { episode: 5, available: false }),
})
const zodiac = itemEntry(10, {
  title: 'Zodiac',
  progress: { position_s: 3000, completed: false },
  collection_ids: [7],
})
const homeVideo = itemEntry(11, { type: 'video', title: 'Home video', duration_s: 120 })
const pendingEpisode = itemEntry(12, { type: 'episode', title: 'Andor S01E01', duration_s: 2400 })
const ghost = itemEntry(13, { title: 'Ghost', available: false })

function fixture(): LibraryMock {
  return {
    entries: [masterplan, darkMatter, zodiac, monster, invincible, homeVideo, pendingEpisode, ghost],
    continueWatching: [darkMatter, zodiac],
    collections: [{ id: 7, name: 'Watchlist', item_count: 1, thumb_urls: [] }],
    shows: {
      1: showDetail(masterplan),
      2: showDetail(darkMatter),
    },
  }
}

const grid = (page: Page) => page.locator('[data-library-grid]')
const continueRow = (page: Page) => page.locator('[data-continue-watching]')

test('the mixed grid renders splash, spine, movie and pending-episode cards', async ({ page }) => {
  await mockLibraryApi(page, fixture())
  await page.goto('/')
  const cards = grid(page)

  const splash = cards.getByRole('link', { name: 'Open Masterplan' })
  await expect(splash).toHaveAttribute('href', '/shows/1')
  await expect(splash).toContainText('6 episodes')
  await expect(splash.locator('img')).toHaveCSS('object-position', '0% 50%')
  await expect(cards.getByRole('link', { name: /^Play Masterplan/ })).toHaveCount(0)

  await expect(cards.getByRole('link', { name: 'Open Dark Matter' })).toHaveAttribute('href', '/shows/2')
  const still = cards.getByRole('link', { name: 'Play Dark Matter S2 E2' })
  await expect(still).toHaveAttribute('href', '/watch/202')
  await expect(still).toContainText('S2 E2')

  // No poster and no art: the spine is still there, and the card is flagged.
  const monsterSpine = cards.getByRole('link', { name: 'Open Monster' })
  await expect(monsterSpine).toHaveAttribute('href', '/shows/3')
  await expect(monsterSpine.locator('img')).toHaveCount(0)
  await expect(monsterSpine.locator('..').getByText('Check', { exact: true })).toBeVisible()

  // An offline up-next opens the show page and reads Offline.
  const offlineStill = cards.getByRole('link', { name: 'Open Invincible S1 E5' })
  await expect(offlineStill).toHaveAttribute('href', '/shows/4')
  await expect(offlineStill.locator('..').getByText('Offline', { exact: true })).toBeVisible()

  await expect(cards.getByRole('link', { name: 'Play Zodiac' })).toHaveAttribute('href', '/watch/10')
  await expect(cards.getByRole('link', { name: 'Play Home video' })).toHaveAttribute('href', '/watch/11')
  await expect(cards.getByRole('link', { name: 'Play Andor S01E01' })).toHaveAttribute('href', '/watch/12')

  // Offline is full colour plus an explicit label, never greyscale.
  const ghostCard = cards.getByRole('link', { name: 'Open Ghost, offline' })
  await expect(ghostCard).toHaveAttribute('href', '/items/13')
  await expect(ghostCard.getByText('Offline', { exact: true })).toBeVisible()
  await expect(ghostCard.locator('img')).toHaveCSS('filter', 'none')
  await expect(ghostCard.locator('img')).toHaveCSS('opacity', '1')
  await expect(page.locator('.grayscale, [class*="opacity-40"]')).toHaveCount(0)

  // Movies and videos never carry a spine.
  await expect(cards.getByRole('link', { name: /^Open (Zodiac|Home video|Andor)/ })).toHaveCount(0)
})

test('the type tabs scope only the grid; Continue watching stays unchanged', async ({ page }) => {
  const log = await mockLibraryApi(page, fixture())
  await page.goto('/')

  const tabs = page.getByRole('tablist', { name: 'Show' })
  for (const name of ['All', 'Shows', 'Movies']) {
    const box = await tabs.getByRole('tab', { name }).boundingBox()
    expect(box?.height).toBeGreaterThanOrEqual(44)
    expect(box?.width).toBeGreaterThanOrEqual(44)
  }

  const row = continueRow(page)
  await expect(row.getByRole('link')).toHaveCount(3)
  const before = await row.getByRole('link').evaluateAll((links) =>
    links.map((link) => `${link.getAttribute('aria-label')} ${link.getAttribute('href')}`),
  )
  expect(before).toEqual(['Open Dark Matter /shows/2', 'Play Dark Matter S2 E2 /watch/202', 'Resume Zodiac /watch/10'])

  const expectRowUnchanged = async () => {
    await expect(row).toBeVisible()
    await expect
      .poll(() =>
        row
          .getByRole('link')
          .evaluateAll((links) => links.map((link) => `${link.getAttribute('aria-label')} ${link.getAttribute('href')}`)),
      )
      .toEqual(before)
  }

  await tabs.getByRole('tab', { name: 'Shows' }).click()
  await expect(page).toHaveURL(/[?&]type=show(&|$)/)
  await expect(tabs.getByRole('tab', { name: 'Shows' })).toHaveAttribute('aria-selected', 'true')
  await expect(grid(page).getByRole('link', { name: 'Play Zodiac' })).toHaveCount(0)
  await expect(grid(page).getByRole('link', { name: 'Open Masterplan' })).toBeVisible()
  expect(log.libraryRequests.some((p) => p.get('type') === 'show' && p.get('in_progress') == null)).toBe(true)
  await expectRowUnchanged()

  await tabs.getByRole('tab', { name: 'Movies' }).click()
  await expect(page).toHaveURL(/[?&]type=movie(&|$)/)
  await expect(grid(page).getByRole('link', { name: 'Open Masterplan' })).toHaveCount(0)
  await expect(grid(page).getByRole('link', { name: 'Play Zodiac' })).toBeVisible()
  await expect(grid(page).getByRole('link', { name: 'Play Home video' })).toHaveCount(0)
  await expectRowUnchanged()

  await tabs.getByRole('tab', { name: 'All' }).click()
  await expect(page).not.toHaveURL(/type=/)
  await expectRowUnchanged()
  expect(log.libraryRequests.every((p) => p.get('in_progress') !== '1' || p.get('type') == null)).toBe(true)

  // The tabs compose with the collection chips, which still hide the row.
  await page.getByRole('button', { name: 'Watchlist' }).click()
  await tabs.getByRole('tab', { name: 'Movies' }).click()
  await expect(page).toHaveURL(/collection=7/)
  await expect(page).toHaveURL(/type=movie/)
  await expect(row).toHaveCount(0)
  await expect(grid(page).getByRole('link', { name: 'Play Zodiac' })).toBeVisible()
})

test('a spine opens the show page, its still plays, and a splash card opens the show page', async ({ page }) => {
  await mockLibraryApi(page, fixture())
  await page.goto('/')

  await grid(page).getByRole('link', { name: 'Open Dark Matter' }).click()
  await expect(page).toHaveURL('/shows/2')
  await expect(page.getByRole('heading', { level: 1, name: 'Dark Matter' })).toBeVisible()

  await page.goBack()
  await grid(page).getByRole('link', { name: 'Open Masterplan' }).click()
  await expect(page).toHaveURL('/shows/1')
  await expect(page.getByRole('heading', { level: 1, name: 'Masterplan' })).toBeVisible()

  await page.goBack()
  await grid(page).getByRole('link', { name: 'Play Dark Matter S2 E2' }).click()
  await expect(page).toHaveURL('/watch/202')
})

test('selection takes show cards: add to collection and trash the whole show', async ({ page }) => {
  const log = await mockLibraryApi(page, fixture())
  await page.goto('/')

  await page.getByRole('button', { name: 'Select', exact: true }).click()
  await page.getByRole('checkbox', { name: 'Select Dark Matter' }).click()
  await page.getByRole('checkbox', { name: 'Select Home video' }).click()
  await expect(page.getByRole('status').filter({ hasText: 'selected' })).toHaveText('2 selected')

  await page.getByRole('button', { name: 'Add selected to collection' }).click()
  await page.getByRole('menuitem', { name: 'Watchlist' }).click()
  await expect.poll(() => log.collectionAdds).toEqual([
    { collectionID: 7, body: { show_id: 2 } },
    { collectionID: 7, body: { item_id: 11 } },
  ])
  await expect(page.getByText('Added 1 show and 1 item to Watchlist')).toBeVisible()

  await page.getByRole('button', { name: 'Select', exact: true }).click()
  await page.getByRole('checkbox', { name: 'Select Masterplan' }).click()
  await page.getByRole('checkbox', { name: 'Select Ghost' }).click()
  await page.getByRole('button', { name: 'Move to trash' }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toContainText('1 show and 1 item will move to the root')
  await expect(dialog).toContainText('Shows go with all their episodes.')
  await dialog.getByRole('button', { name: 'Move to trash' }).click()
  await expect.poll(() => log.showTrash).toEqual([1])
  await expect.poll(() => log.itemDeletes).toEqual([13])

  await page.getByRole('button', { name: 'Undo' }).click()
  await expect.poll(() => [...log.restores].sort((a, b) => a - b)).toEqual([13, 101, 102])
})
