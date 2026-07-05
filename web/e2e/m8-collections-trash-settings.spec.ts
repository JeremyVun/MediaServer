import { expect, test, type Page, type Route } from '@playwright/test'

type Item = ReturnType<typeof summary>

type Collection = {
  id: number
  name: string
  item_count: number
  thumb_urls: string[]
}

type Job = {
  id: number
  type: string
  payload: string
  status: 'failed' | 'queued'
  attempts: number
  run_at: string
  started_at: string | null
  finished_at: string | null
  error: string | null
}

type MockState = {
  items: Item[]
  trash: Item[]
  collections: Collection[]
  order: number[]
  restored: number[]
  retried: number[]
  failedJobs: Job[]
}

test('collections reorder and settings trash jobs controls', async ({ page }) => {
  const state = await mockM8Server(page)

  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'Library' })).toBeVisible()
  await page.getByRole('button', { name: 'Watchlist' }).click()
  await expect(page.getByRole('link', { name: /First/ })).toBeVisible()

  await page.goto('/collections/1')
  await expect(page.getByRole('heading', { name: 'Watchlist' })).toBeVisible()
  await page.getByRole('button', { name: 'Move later' }).first().click()
  await expect.poll(() => state.order).toEqual([2, 1])

  await page.goto('/settings')
  await expect(page.getByRole('heading', { name: 'Trash' })).toBeVisible()
  await page.getByRole('button', { name: 'Restore' }).click()
  await expect.poll(() => state.restored).toEqual([9])

  await page.getByRole('button', { name: 'Retry' }).click()
  await expect.poll(() => state.retried).toEqual([42])
})

async function mockM8Server(page: Page): Promise<MockState> {
  const state: MockState = {
    items: [summary(1, 'First'), summary(2, 'Second')],
    trash: [summary(9, 'Deleted Movie')],
    collections: [
      {
        id: 1,
        name: 'Watchlist',
        item_count: 2,
        thumb_urls: ['/api/items/1/thumb', '/api/items/2/thumb'],
      },
    ],
    order: [1, 2],
    restored: [],
    retried: [],
    failedJobs: [
      {
        id: 42,
        type: 'probe',
        payload: '{"root_id":1}',
        status: 'failed',
        attempts: 1,
        run_at: '2026-07-05 00:00:00',
        started_at: null,
        finished_at: '2026-07-05 00:01:00',
        error: 'Invalid data',
      },
    ],
  }

  await page.route('**/*', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname
    if (!path.startsWith('/api/')) {
      await route.continue()
      return
    }

    if (request.method() === 'GET' && path === '/api/events') {
      await route.fulfill({ status: 204 })
      return
    }

    if (request.method() === 'GET' && path === '/api/health') {
      await json(route, {
        version: 'test',
        uptime_s: 120,
        db_ok: true,
        roots: [{ id: 1, name: 'Media A', online: true, free_bytes: 100_000_000 }],
        active_sessions: 0,
        queue_depth: 1,
      })
      return
    }

    if (request.method() === 'GET' && path === '/api/roots') {
      await json(route, [
        {
          id: 1,
          name: 'Media A',
          path: '/Volumes/Media-A',
          online: true,
          free_bytes: 100_000_000,
          file_count: 2,
        },
      ])
      return
    }

    if (request.method() === 'GET' && path === '/api/collections') {
      await json(route, state.collections)
      return
    }

    if (request.method() === 'GET' && path === '/api/items') {
      const trashed = url.searchParams.get('trashed') === '1'
      const collectionID = Number(url.searchParams.get('collection_id') ?? 0)
      const base = trashed ? state.trash : state.items
      const items =
        collectionID > 0
          ? state.order.map((id) => base.find((item) => item.id === id)).filter(Boolean)
          : base
      await json(route, { total: items.length, items })
      return
    }

    const thumbMatch = path.match(/^\/api\/items\/(\d+)\/thumb$/)
    if (request.method() === 'GET' && thumbMatch) {
      await json(
        route,
        { error: { code: 'thumbnail_not_ready', message: 'thumbnail has not been generated yet' } },
        416,
      )
      return
    }

    if (request.method() === 'PUT' && path === '/api/collections/1/order') {
      const body = JSON.parse(request.postData() ?? '{}') as { item_ids: number[] }
      state.order = body.item_ids
      await route.fulfill({ status: 204 })
      return
    }

    if (request.method() === 'POST' && path === '/api/items/9/restore') {
      state.restored.push(9)
      state.trash = []
      await json(route, { id: 9 })
      return
    }

    if (request.method() === 'GET' && path === '/api/jobs') {
      await json(route, state.failedJobs)
      return
    }

    if (request.method() === 'POST' && path === '/api/jobs/42/retry') {
      state.retried.push(42)
      const retried = { ...state.failedJobs[0], status: 'queued' as const }
      state.failedJobs = []
      await json(route, retried, 202)
      return
    }

    await json(route, { error: { code: 'not_found', message: 'no such endpoint' } }, 404)
  })

  return state
}

function summary(id: number, title: string) {
  return {
    id,
    type: 'video' as const,
    title,
    year: 2026,
    duration_s: 120,
    created_at: '2026-07-05 00:00:00',
    thumb_url: `/api/items/${id}/thumb`,
    available: true,
    collection_ids: id === 9 ? [] : [1],
  }
}

async function json(route: Route, body: unknown, status = 200) {
  await route.fulfill({
    status,
    contentType: 'application/json',
    body: JSON.stringify(body),
  })
}
