import { expect, test, type Page, type Route } from '@playwright/test'

type Item = ReturnType<typeof summary>
type Root = {
  id: number
  name: string
  online: boolean
  free_bytes: number
}

type MockState = {
  items: Item[]
  roots: Root[]
}

declare global {
  interface Window {
    __emitSSE: (type: string, data: unknown) => void
  }
}

test('SSE updates refresh the library and show root status toast', async ({ page }) => {
  await installEventSourceShim(page)
  const state = await mockLiveServer(page)

  await page.goto('/')
  await expect(page.getByRole('link', { name: /Big Buck Bunny/ })).toBeVisible()

  const added = summary(2, 'New Discovery', true)
  state.items = [added, ...state.items]
  await page.evaluate((item) => window.__emitSSE('item.added', item), added)

  await expect(page.getByRole('link', { name: /New Discovery/ })).toBeVisible()
  await expect(page.getByText('New', { exact: true })).toBeVisible()

  state.roots = [{ ...state.roots[0], online: false }]
  state.items = state.items.map((item) => ({ ...item, available: false }))
  await page.evaluate(() => window.__emitSSE('root.status', { id: 1, online: false }))

  await expect(page.getByText('Media A disconnected')).toBeVisible()
})

async function installEventSourceShim(page: Page) {
  await page.addInitScript(() => {
    class MockEventSource extends EventTarget {
      static instances: MockEventSource[] = []
      onopen: ((event: Event) => void) | null = null
      onerror: ((event: Event) => void) | null = null
      readonly url: string
      readyState = 0

      constructor(url: string | URL) {
        super()
        this.url = String(url)
        MockEventSource.instances.push(this)
        window.setTimeout(() => {
          this.readyState = 1
          const event = new Event('open')
          this.onopen?.(event)
          this.dispatchEvent(event)
        }, 0)
      }

      close() {
        this.readyState = 2
        MockEventSource.instances = MockEventSource.instances.filter((source) => source !== this)
      }
    }

    Object.defineProperty(window, 'EventSource', {
      configurable: true,
      value: MockEventSource,
    })

    window.__emitSSE = (type: string, data: unknown) => {
      const event = new MessageEvent(type, { data: JSON.stringify(data) })
      for (const source of MockEventSource.instances) {
        source.dispatchEvent(event)
      }
    }
  })
}

async function mockLiveServer(page: Page): Promise<MockState> {
  const state: MockState = {
    // Seeded before the 24 h "New" window so only the SSE arrival is badged.
    items: [{ ...summary(1, 'Big Buck Bunny', true), created_at: '2026-01-01 00:00:00' }],
    roots: [{ id: 1, name: 'Media A', online: true, free_bytes: 100_000_000 }],
  }

  await page.route('**/*', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname
    if (!path.startsWith('/api/')) {
      await route.continue()
      return
    }

    if (request.method() === 'GET' && path === '/api/health') {
      await json(route, {
        version: 'test',
        uptime_s: 12,
        db_ok: true,
        roots: state.roots,
        active_sessions: 0,
        queue_depth: 0,
      })
      return
    }

    if (request.method() === 'GET' && path === '/api/items') {
      await json(route, {
        total: state.items.length,
        items: state.items,
      })
      return
    }

    if (request.method() === 'GET' && path.startsWith('/api/items/') && path.endsWith('/thumb')) {
      await json(
        route,
        { error: { code: 'thumbnail_not_ready', message: 'thumbnail has not been generated yet' } },
        416,
      )
      return
    }

    await json(route, { error: { code: 'not_found', message: 'no such endpoint' } }, 404)
  })

  return state
}

function summary(id: number, title: string, available: boolean) {
  return {
    id,
    type: 'video',
    title,
    year: 2026,
    duration_s: 120,
    // Server format: "YYYY-MM-DD HH:MM:SS" UTC. Fresh so the New badge shows.
    created_at: new Date().toISOString().slice(0, 19).replace('T', ' '),
    thumb_url: `/api/items/${id}/thumb`,
    available,
    collection_ids: [],
  }
}

async function json(route: Route, body: unknown, status = 200) {
  await route.fulfill({
    status,
    contentType: 'application/json',
    body: JSON.stringify(body),
  })
}
