import { expect, test, type Page } from '@playwright/test'
import type { ShowDetail, ShowEpisodeEntry } from '../src/api/types.ts'
import { itemEntry, json, mockLibraryApi, showDetail, showEntry, upNext, type LibraryLog } from './support/library'

const SHOW_ID = 5

function episode(id: number, n: number, overrides: Partial<ReturnType<typeof itemEntry>> = {}): ShowEpisodeEntry {
  return {
    ...itemEntry(id, {
      type: 'episode',
      title: `Episode title ${n}`,
      duration_s: 3000,
      show: { id: SHOW_ID, title: 'Furious' },
      season: 1,
      episode: n,
    }),
    ...overrides,
  }
}

function furious(overrides: Partial<ShowDetail> = {}): ShowDetail {
  return showDetail(
    showEntry(SHOW_ID, {
      title: 'Furious',
      year: 2026,
      network: 'Hulu',
      season_count: 2,
      episode_count: 3,
      needs_check: true,
      has_progress: true,
      up_next: upNext(502, { season: 1, episode: 2, progress: { position_s: 600, completed: false } }),
    }),
    {
      summary: 'An FBI agent hunts a serial killer.',
      tvmaze_id: 81234,
      seasons: [
        {
          season: 1,
          episodes: [
            episode(501, 1, { progress: { position_s: 2990, completed: true } }),
            episode(502, 2, { progress: { position_s: 600, completed: false } }),
            episode(503, 3),
            { kind: 'missing', season: 1, number: 4, title: 'Flash Flood', airdate: '2020-08-03', missing: true },
            { kind: 'missing', season: 1, number: 5, title: 'Soon', airdate: '2099-10-23', missing: true },
          ],
        },
        {
          season: 2,
          episodes: [1, 2, 3].map((n) => ({
            kind: 'missing' as const,
            season: 2,
            number: n,
            title: `Later ${n}`,
            airdate: '2021-05-0' + n,
            missing: true as const,
          })),
        },
      ],
      ...overrides,
    },
  )
}

interface Requests {
  patches: unknown[]
  matches: unknown[]
  merges: unknown[]
  seasonLookups: number[]
  tvmazeRequests: string[]
  log: LibraryLog
}

async function setup(page: Page, options: { matchTaken?: boolean; show?: ShowDetail } = {}): Promise<Requests> {
  let show = options.show ?? furious()
  const andor = showDetail(showEntry(9, { title: 'Andor', episode_count: 12 }))
  const other = showDetail(showEntry(10, { title: 'Below', episode_count: 6 }))
  const requests: Omit<Requests, 'log'> = { patches: [], matches: [], merges: [], seasonLookups: [], tvmazeRequests: [] }

  page.on('request', (request) => {
    if (new URL(request.url()).hostname === 'api.tvmaze.com') requests.tvmazeRequests.push(request.url())
  })

  const log = await mockLibraryApi(
    page,
    { entries: [], continueWatching: [], collections: [], shows: { 9: andor, 10: other } },
    async (route, path, url) => {
      const method = route.request().method()
      if (path === `/api/shows/${SHOW_ID}` && method === 'GET') {
        await json(route, show)
        return true
      }
      if (path === `/api/shows/${SHOW_ID}` && method === 'PATCH') {
        const body = route.request().postDataJSON()
        requests.patches.push(body)
        show = { ...show, ...(body.confirmed ? { needs_check: false } : {}), ...(body.title ? { title: body.title } : {}) }
        await json(route, show)
        return true
      }
      if (path === `/api/shows/${SHOW_ID}/match`) {
        requests.matches.push(route.request().postDataJSON())
        if (options.matchTaken) {
          await json(route, { error: { code: 'match_taken', message: 'taken' }, show_id: 9 }, 409)
          return true
        }
        show = { ...show, needs_check: false }
        await json(route, show)
        return true
      }
      if (path === `/api/shows/${SHOW_ID}/merge`) {
        requests.merges.push(route.request().postDataJSON())
        await json(route, andor)
        return true
      }
      if (path === '/api/shows/match-seasons') {
        requests.seasonLookups.push(Number(url.searchParams.get('tvmaze_id')))
        await json(route, { seasons: url.searchParams.get('tvmaze_id') === '2' ? [2, 3] : [1, 2] })
        return true
      }
      if (path === '/api/shows/match-search') {
        expect(url.searchParams.get('q')).toBeTruthy()
        await json(route, {
          results: [
            { tvmaze_id: 1, name: 'Furious', year: 2026, network: 'Hulu', poster_url: null, local_show_id: null },
            { tvmaze_id: 2, name: 'Furious Women', year: 2019, network: null, poster_url: null, local_show_id: null },
          ],
        })
        return true
      }
      if (path === '/api/library' && url.searchParams.get('type') === 'show') {
        await json(route, {
          total: 3,
          entries: [
            { kind: 'show', ...andor },
            { kind: 'show', ...other },
            { kind: 'show', ...show },
          ],
        })
        return true
      }
      return false
    },
  )
  return { ...requests, log }
}

const tiles = (page: Page) => page.locator('[data-tiles]')

/** Present, missing and unaired tiles share one box per grid cell: equal
    size, every column at one x and every row at one y (within 0.5 px). */
async function expectTileGrid(page: Page) {
  const boxes = await tiles(page)
    .locator(':scope > *')
    .evaluateAll((els) => els.map((el) => el.getBoundingClientRect().toJSON() as DOMRect))
  const near = (a: number, b: number) => Math.abs(a - b) <= 0.5
  const columns = boxes.filter((b) => near(b.y, boxes[0].y)).map((b) => b.x)
  for (const [i, b] of boxes.entries()) {
    expect(near(b.width, boxes[0].width) && near(b.height, boxes[0].height), `tile ${i} size`).toBe(true)
    expect(near(b.x, columns[i % columns.length]), `tile ${i} x`).toBe(true)
    expect(near(b.y, boxes[i - (i % columns.length)].y), `tile ${i} y`).toBe(true)
  }
}

test('renders the up-next season with missing and unaired tiles, and switches seasons in place', async ({ page }) => {
  await setup(page)
  await page.goto(`/shows/${SHOW_ID}`)

  await expect(page.getByRole('heading', { name: 'Furious' })).toBeVisible()
  await expect(page.getByText('2026 · Hulu · 2 seasons')).toBeVisible()
  await expect(page.getByRole('tab', { name: 'Season 1' })).toHaveAttribute('aria-selected', 'true')

  const missing = tiles(page).locator('[data-state="missing"]')
  await expect(missing).toHaveText(/4Flash FloodMissing · Aired Aug 2020/)
  await expect(missing.locator('a, button, img')).toHaveCount(0)
  const unaired = tiles(page).locator('[data-state="upcoming"]')
  await expect(unaired).toHaveText(/Airs Fri 23 Oct 2099/)
  await expect(unaired).not.toContainText('Missing')
  await expect(tiles(page).getByRole('link', { name: '1 Episode title 1' })).toContainText('Watched')
  await expect(tiles(page).getByRole('link', { name: '2 Episode title 2' })).toContainText('40 min left')

  await expectTileGrid(page)

  const url = page.url()
  await missing.click({ force: true })
  expect(page.url()).toBe(url)

  await page.getByRole('tab', { name: 'Season 2, none in your library' }).click()
  await expect(tiles(page)).toHaveAttribute('data-season', '2')
  await expect(tiles(page).locator(':scope > *')).toHaveCount(1)
  await expect(tiles(page)).toHaveText(/All 3 missing/)
  await expect(page).toHaveURL(/season=2/)
  await expect(page.getByText('Show data from TVmaze')).toBeVisible()
})

test('Resume plays the up-next episode', async ({ page }) => {
  await setup(page)
  await page.goto(`/shows/${SHOW_ID}`)
  await page.getByRole('button', { name: 'Resume S1 E2' }).click()
  await expect(page).toHaveURL(/\/watch\/502$/)
})

test('a tile plays its episode', async ({ page }) => {
  await setup(page)
  await page.goto(`/shows/${SHOW_ID}`)
  await tiles(page).getByRole('link', { name: '3 Episode title 3' }).click()
  await expect(page).toHaveURL(/\/watch\/503$/)
})

test('Looks right confirms the match and hides the strip', async ({ page }) => {
  const requests = await setup(page)
  await page.goto(`/shows/${SHOW_ID}`)
  const strip = page.locator('[data-check-strip]')
  await expect(strip).toContainText('Matched to Furious (2026) on TVmaze')
  await strip.getByRole('button', { name: 'Looks right' }).click()
  await expect(strip).toHaveCount(0)
  expect(requests.patches).toEqual([{ confirmed: true }])
})

test('an unmatched show offers only Change, with Not on TVmaze selected', async ({ page }) => {
  await setup(page, { show: furious({ tvmaze_id: null }) })
  await page.goto(`/shows/${SHOW_ID}`)
  const strip = page.locator('[data-check-strip]')
  await expect(strip).toContainText('Not matched on TVmaze')
  await expect(strip.getByRole('button', { name: 'Looks right' })).toHaveCount(0)
  await strip.getByRole('button', { name: 'Change' }).click()
  const dialog = page.getByRole('dialog', { name: 'Change match' })
  await expect(dialog.getByRole('radio').first()).toContainText('Furious')
  await expect(dialog.getByRole('radio', { name: /Not on TVmaze/ })).toHaveAttribute('aria-checked', 'true')
  await expect(page.getByText('Show data from TVmaze')).toHaveCount(0)
})

test('Change matches to a candidate', async ({ page }) => {
  const requests = await setup(page)
  await page.goto(`/shows/${SHOW_ID}`)
  await page.locator('[data-check-strip]').getByRole('button', { name: 'Change' }).click()
  const dialog = page.getByRole('dialog', { name: 'Change match' })
  await expect(dialog.getByRole('searchbox', { name: 'Search TVmaze' })).toHaveValue('Furious')
  await dialog.getByRole('radio', { name: /Furious Women/ }).click()
  await expect(dialog.getByText('Files say season 1')).toBeVisible()
  await expect(dialog.getByRole('combobox')).toHaveValue('2')
  await dialog.getByRole('radio').filter({ hasText: 'Hulu' }).click()
  await expect(dialog.getByText('Files say season 1')).toHaveCount(0)
  await dialog.getByRole('button', { name: 'Save' }).click()
  await expect(dialog).toBeHidden()
  expect(requests.matches).toEqual([{ tvmaze_id: 1, season_offset: 0 }])
  expect(requests.seasonLookups).toEqual([81234, 2, 1])
  expect(requests.tvmazeRequests).toEqual([])
})

test('Change sends a season offset when TVmaze numbers the season differently', async ({ page }) => {
  const requests = await setup(page)
  await page.goto(`/shows/${SHOW_ID}`)
  await page.locator('[data-check-strip]').getByRole('button', { name: 'Change' }).click()
  const dialog = page.getByRole('dialog', { name: 'Change match' })
  await dialog.getByRole('radio', { name: /Furious Women/ }).click()
  await dialog.getByRole('combobox').selectOption('3')
  await dialog.getByRole('button', { name: 'Save' }).click()
  await expect(dialog).toBeHidden()
  expect(requests.matches).toEqual([{ tvmaze_id: 2, season_offset: 2 }])
})

test('a taken match offers a merge into the show holding it', async ({ page }) => {
  const requests = await setup(page, { matchTaken: true })
  await page.goto(`/shows/${SHOW_ID}`)
  await page.locator('[data-check-strip]').getByRole('button', { name: 'Change' }).click()
  const dialog = page.getByRole('dialog', { name: 'Change match' })
  await dialog.getByRole('radio').filter({ hasText: 'Hulu' }).click()
  await dialog.getByRole('button', { name: 'Save' }).click()
  await expect(dialog.getByRole('status')).toContainText('Furious is already one of your shows')
  await dialog.getByRole('button', { name: 'Merge' }).click()
  await expect(page).toHaveURL(/\/shows\/9$/)
  expect(requests.merges).toEqual([{ into_show_id: 9 }])
})

test('Merge moves the show into a picked show', async ({ page }) => {
  const requests = await setup(page)
  await page.goto(`/shows/${SHOW_ID}`)
  await page.getByRole('button', { name: 'More' }).click()
  await page.getByRole('menuitem', { name: 'Merge into another show' }).click()
  const dialog = page.getByRole('dialog', { name: 'Merge into another show' })
  await expect(dialog).toContainText("Furious's 3 episodes move to the show you pick")
  await expect(dialog.getByRole('radio')).toHaveCount(2)
  const merge = dialog.getByRole('button', { name: 'Merge' })
  await expect(merge).toBeDisabled()
  await dialog.getByRole('searchbox', { name: 'Search your shows' }).fill('and')
  await dialog.getByRole('radio', { name: /Andor/ }).click()
  await merge.click()
  await expect(page).toHaveURL(/\/shows\/9$/)
  expect(requests.merges).toEqual([{ into_show_id: 9 }])
})

test('Rename sends the new title', async ({ page }) => {
  const requests = await setup(page)
  await page.goto(`/shows/${SHOW_ID}`)
  await page.getByRole('button', { name: 'More' }).click()
  await page.getByRole('menuitem', { name: 'Rename' }).click()
  const dialog = page.getByRole('dialog', { name: 'Rename show' })
  await dialog.getByRole('textbox', { name: 'Show name' }).fill('Furious (US)')
  await dialog.getByRole('button', { name: 'Save' }).click()
  await expect(page.getByRole('heading', { name: 'Furious (US)' })).toBeVisible()
  expect(requests.patches).toEqual([{ title: 'Furious (US)' }])
})

test('Move to trash trashes the show, and Undo restores every episode', async ({ page }) => {
  const requests = await setup(page)
  await page.goto(`/shows/${SHOW_ID}`)
  await page.getByRole('button', { name: 'More' }).click()
  await page.getByRole('menuitem', { name: 'Move to trash' }).click()
  await page.getByRole('dialog', { name: 'Move to trash?' }).getByRole('button', { name: 'Move to trash' }).click()
  await expect(page).toHaveURL(/\/$/)
  expect(requests.log.showTrash).toEqual([SHOW_ID])
  await page.getByRole('button', { name: 'Undo' }).click()
  await expect.poll(() => requests.log.restores).toEqual([501, 502])
})

test('an unknown show reads Show not found', async ({ page }) => {
  await setup(page)
  await page.goto('/shows/77')
  await expect(page.getByText('Show not found')).toBeVisible({ timeout: 8_000 })
})

function seasonsShow(): ShowDetail {
  const missing = (season: number, number: number) => ({
    kind: 'missing' as const, season, number, title: `Episode ${number}`, airdate: `20${String(season).padStart(2, '0')}-03-0${number}`, missing: true as const,
  })
  const seasons = Array.from({ length: 22 }, (_, i) => i + 1).map((season) => ({
    season,
    episodes:
      season === 21
        ? Array.from({ length: 12 }, (_, i) => episode(2100 + i, i + 1, { season: 21, title: i === 9 ? 'A long title beside a two-digit number' : `Episode title ${i + 1}` }))
        : [1, 2, 3].map((n) => missing(season, n)),
  }))
  return furious({
    season_count: 22,
    needs_check: false,
    summary: Array.from({ length: 12 }, () => 'A long summary that keeps going so the clamp has work to do.').join(' '),
    up_next: upNext(2106, { season: 21, episode: 7 }),
    seasons,
  })
}

test.describe('on a phone', () => {
  test.use({ viewport: { width: 390, height: 844 } })

  test('22 seasons open on the up-next chip, fully visible, with no page overflow', async ({ page }) => {
    await setup(page, { show: seasonsShow() })
    await page.goto(`/shows/${SHOW_ID}`)
    const row = page.getByRole('tablist', { name: 'Seasons' })
    const selected = row.getByRole('tab', { selected: true })
    await expect(selected).toHaveAccessibleName('Season 21')
    await expect(selected).toHaveText('S21')
    expect(await row.evaluate((el) => el.scrollWidth > el.clientWidth)).toBe(true)
    await expect
      .poll(async () => {
        const [r, c] = await Promise.all([row.boundingBox(), selected.locator('span').boundingBox()])
        return !!r && !!c && c.x >= r.x && c.x + c.width <= r.x + r.width && c.x + c.width <= 390
      })
      .toBe(true)
    await expect(row.getByRole('tab', { name: 'Season 1, none in your library' }).locator('span')).toHaveCSS('border-style', 'dashed')
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(390)
    await expectTileGrid(page)

    // A two-digit number keeps its 6 px gap before the title.
    const foot = tiles(page).getByRole('link', { name: /^10 / }).locator('span > span').first()
    const [number, title] = await foot.locator(':scope > span').evaluateAll((els) => els.map((el) => el.getBoundingClientRect().toJSON() as DOMRect))
    expect(title.x - (number.x + number.width)).toBeGreaterThanOrEqual(5.5)
  })

  test('the summary clamps to 3 lines and expands on tap; tiles carry no description', async ({ page }) => {
    await setup(page, { show: seasonsShow() })
    await page.goto(`/shows/${SHOW_ID}`)
    const summary = page.locator('[data-hero] h1 ~ button').first()
    const lines = async () =>
      summary.evaluate((el) => Math.round(el.getBoundingClientRect().height / parseFloat(getComputedStyle(el).lineHeight)))
    expect(await lines()).toBe(3)
    await summary.click()
    expect(await lines()).toBeGreaterThan(3)

    const tile = tiles(page).getByRole('link').first()
    await expect(tile).toHaveText(/^1Episode title 1\d+ min$/)
  })
})

test('the Check strip is opaque, and Change keeps Not on TVmaze in view on open', async ({ page }) => {
  const results = Array.from({ length: 8 }, (_, i) => ({ tvmaze_id: 100 + i, name: `Furious ${i}`, year: 2000 + i, network: 'Hulu', poster_url: null, local_show_id: null }))
  await setup(page, { show: furious({ tvmaze_id: null }) })
  await page.route(
    (url) => url.pathname === '/api/shows/match-search',
    (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ results }) }),
  )
  await page.goto(`/shows/${SHOW_ID}`)
  await expect(page.locator('[data-check-strip]')).toHaveCSS('background-color', /^rgb\(/)

  await page.locator('[data-check-strip]').getByRole('button', { name: 'Change' }).click()
  const dialog = page.getByRole('dialog', { name: 'Change match' })
  await expect(dialog.getByRole('radio', { name: 'Furious 7' })).toBeAttached()
  const none = dialog.getByRole('radio', { name: /Not on TVmaze/ })
  await expect(none).toHaveAttribute('aria-checked', 'true')
  await expect(none).toBeInViewport({ ratio: 1 })
  await expect(dialog.getByRole('radio', { name: 'Furious 7' })).not.toBeInViewport()
})
