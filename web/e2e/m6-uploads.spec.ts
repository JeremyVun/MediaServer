import { expect, test, type Page, type Route } from '@playwright/test'
import { Buffer } from 'node:buffer'

type Item = ReturnType<typeof summary>

type MockState = {
  items: Item[]
  upload: {
    id: string
    filename: string
    size: number
    received: number
  } | null
}

declare global {
  interface Window {
    __emitSSE: (type: string, data: unknown) => void
  }
}

test('upload tray completes and links to the ingested item', async ({ page }) => {
  await installEventSourceShim(page)
  const state = await mockUploadServer(page)
  const file = Buffer.from('fake mp4 payload')

  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'Library' })).toBeVisible()

  const chooserPromise = page.waitForEvent('filechooser')
  await page.getByRole('button', { name: 'Upload' }).click()
  const chooser = await chooserPromise
  await chooser.setFiles({ name: 'Upload.Sample.mp4', mimeType: 'video/mp4', buffer: file })

  await expect.poll(() => state.upload?.received ?? 0).toBe(file.length)
  await expect(page.getByText('Processing...')).toBeVisible()

  const item = summary(7, 'Upload Sample')
  state.items = [item]
  await page.evaluate((payload) => window.__emitSSE('upload.complete', payload), {
    id: 'upload-1',
    item_id: item.id,
  })
  await page.evaluate((payload) => window.__emitSSE('item.added', payload), item)

  await expect(page.getByRole('link', { name: 'Open item' })).toBeVisible()
  await expect(page.getByRole('link', { name: /Upload Sample/ })).toBeVisible()
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

async function mockUploadServer(page: Page): Promise<MockState> {
  const state: MockState = {
    items: [],
    upload: null,
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
        roots: [{ id: 1, name: 'Media A', online: true, free_bytes: 100_000_000 }],
        active_sessions: 0,
        queue_depth: 0,
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
          file_count: 0,
        },
      ])
      return
    }

    if (request.method() === 'GET' && path === '/api/items') {
      await json(route, { total: state.items.length, items: state.items })
      return
    }

    if (request.method() === 'POST' && path === '/api/uploads') {
      const body = JSON.parse(request.postData() ?? '{}') as {
        filename: string
        size: number
      }
      state.upload = {
        id: 'upload-1',
        filename: body.filename,
        size: body.size,
        received: 0,
      }
      await json(route, { id: state.upload.id, chunk_size: 8 * 1024 * 1024 }, 201)
      return
    }

    if (request.method() === 'PUT' && path === '/api/uploads/upload-1') {
      if (!state.upload) throw new Error('upload missing')
      const range = request.headers()['content-range']
      const match = range?.match(/^bytes \d+-(\d+)\/\d+$/)
      if (!match) throw new Error(`bad Content-Range: ${range}`)
      state.upload.received = Number(match[1]) + 1
      await json(route, { received: state.upload.received })
      return
    }

    if (request.method() === 'POST' && path === '/api/uploads/upload-1/complete') {
      await json(route, { item_id: null })
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

function summary(id: number, title: string) {
  return {
    id,
    type: 'video',
    title,
    year: 2026,
    duration_s: 120,
    created_at: new Date().toISOString().slice(0, 19).replace('T', ' '),
    thumb_url: `/api/items/${id}/thumb`,
    available: true,
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
