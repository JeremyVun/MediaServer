import { readFileSync } from 'node:fs'
import { test, type Route } from '@playwright/test'
import { itemEntry, mockLibraryApi, showEntry, upNext } from '../support/library'

const IMG = process.env.EXEMPLAR_IMG ?? '/Users/jeremy/projects/media-server/docs/backlog/shows/comps/r3/workshop/img'
const OUT = process.env.CAPTURE_OUT ?? 'capture-out'
const keys: Record<number, string> = { 1: 'below', 2: 'darkmatter', 3: 'carrie', 4: 'lanterns', 5: 'lastseen', 6: 'eastofeden', 7: 'monster', 8: 'invincible' }
const p = (s: number) => ({ position_s: s, completed: false })
const shows = [
  showEntry(1, { title: 'Below', has_progress: true, up_next: upNext(101, { season: 1, episode: 6, progress: p(1500), duration_s: 3000 }) }),
  showEntry(2, { title: 'Dark Matter', has_progress: true, up_next: upNext(201, { season: 2, episode: 2, progress: p(200), duration_s: 3000 }) }),
  showEntry(3, { title: 'Carrie', has_progress: true, up_next: upNext(301, { season: 1, episode: 8, progress: p(2500), duration_s: 3000 }) }),
  showEntry(4, { title: 'Lanterns', has_progress: true, up_next: upNext(401, { season: 1, episode: 7 }) }),
  showEntry(5, { title: 'Last Seen', episode_count: 8 }),
  showEntry(6, { title: 'East of Eden', episode_count: 7 }),
  showEntry(7, { title: 'Monster The Jeffrey Dahmer Story', poster_url: null, backdrop_url: null, needs_check: true }),
  showEntry(8, { title: 'Invincible', has_progress: true, up_next: upNext(801, { season: 4, episode: 1, available: false }) }),
]
const movies = [
  itemEntry(10, { title: 'Spider-Man: Brand New Day', progress: p(3000) }),
  itemEntry(11, { title: 'Dracula A Love Tale', needs_check: true }),
  itemEntry(12, { title: 'The Uprising', available: false }),
]
const mock = {
  entries: [shows[4], shows[1], shows[0], movies[0], shows[5], shows[2], shows[6], movies[1], shows[3], shows[7], movies[2]],
  continueWatching: [shows[0], shows[1], shows[2], shows[3], movies[0]],
  collections: ['Unsorted?', 'euphoria', 'norsemen', 'movies', 'hotd', 'to watch', 'anime', 'test', 'silo'].slice(1).map((name, i) => ({ id: i + 1, name, item_count: 1, thumb_urls: [] })),
  shows: {},
}

const serve = async (route: Route, file: string) => route.fulfill({ status: 200, contentType: 'image/jpeg', body: readFileSync(file) })

for (const [name, viewport] of [['phone', { width: 390, height: 844 }], ['desktop', { width: 1440, height: 900 }]] as const) {
  for (const scheme of ['dark', 'light'] as const) test(`library ${name} ${scheme}`, async ({ browser }) => {
    const context = await browser.newContext({ viewport, deviceScaleFactor: 2, colorScheme: scheme })
    const page = await context.newPage()
    await mockLibraryApi(page, mock as never, async (route, path) => {
      let m = path.match(/^\/api\/shows\/(\d+)\/(poster|backdrop)$/)
      if (m) { await serve(route, `${IMG}/tvmaze/${keys[Number(m[1])]}-${m[2] === 'poster' ? 'poster' : 'bg'}.jpg`); return true }
      m = path.match(/^\/api\/items\/(\d+)\/thumb$/)
      if (m) {
        const id = Number(m[1])
        const file = id < 100 ? `${IMG}/tmdb/${[100, 101, 102][id - 10]}-backdrop.jpg` : `${IMG}/thumbs/${(id % 97) + 10}.jpg`
        await serve(route, file); return true
      }
      return false
    })
    await page.goto('/')
    await page.locator('[data-library-grid] img').first().waitFor()
    await page.waitForTimeout(1200)
    await page.screenshot({ path: `${OUT}/library-${name}-${scheme}.png` })
    for (const tab of ['Shows', 'Movies']) {
      await page.getByRole('tab', { name: tab }).click()
      await page.waitForTimeout(800)
      await page.screenshot({ path: `${OUT}/${tab.toLowerCase()}-${name}-${scheme}.png` })
    }
    await page.getByRole('tab', { name: 'All' }).click()
    await page.locator('[data-library-grid]').scrollIntoViewIfNeeded()
    await page.waitForTimeout(600)
    await page.screenshot({ path: `${OUT}/library-grid-${name}-${scheme}.png`, fullPage: true })
    await context.close()
  })
}
