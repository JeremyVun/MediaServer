import { expect, test, type Page, type Route } from '@playwright/test'

type ProgressState = {
  position_s: number
  completed: boolean
}

type MockState = {
  progress: ProgressState | null
  playRequests: number
}

test('grid to player saves progress and resumes after reload', async ({ page }) => {
  await installMediaShim(page)
  const state = await mockMediaServer(page)

  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'Library' })).toBeVisible()
  // The poster's play tile also matches /Big Buck Bunny/ via its "Play …"
  // aria-label, so target the details link by href instead of by role name.
  await page.locator('a[href="/items/1"]').click()

  await expect(page.getByRole('heading', { name: 'Big Buck Bunny' })).toBeVisible()
  await page.getByRole('button', { name: 'Play' }).click()

  await expect(page).toHaveURL(/\/watch\/1\?file_id=10&t=0$/)
  await expect.poll(() => state.playRequests).toBe(1)
  await expect(page.getByRole('button', { name: 'Pause' })).toBeVisible()

  const seek = page.getByLabel('Seek')
  await expect.poll(async () => Number(await seek.inputValue())).toBeGreaterThan(0)

  await page.locator('video').evaluate((video: HTMLVideoElement) => video.pause())
  await expect(page.getByRole('button', { name: 'Play', exact: true })).toBeVisible()
  await expect.poll(() => state.progress?.position_s ?? 0).toBeGreaterThan(0)
  const savedPosition = state.progress?.position_s ?? 0

  await page.reload()
  await expect(page.getByRole('button', { name: 'Pause' })).toBeVisible()
  await expect.poll(async () => Number(await seek.inputValue())).toBeGreaterThanOrEqual(savedPosition)

  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'Library' })).toBeVisible()
})

test('player back arrow returns to library home when entered from a poster', async ({ page }) => {
  await installMediaShim(page)
  await mockMediaServer(page)

  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'Library' })).toBeVisible()
  // The poster's play tile jumps straight to /watch, skipping the detail page.
  await page.getByRole('link', { name: /Play Big Buck Bunny/ }).click()

  await expect(page).toHaveURL(/\/watch\/1$/)
  await page.locator('[data-player-controls] a').first().click()
  await expect(page).toHaveURL('/')
  await expect(page.getByRole('heading', { name: 'Library' })).toBeVisible()
})

test('player back arrow returns to item detail when entered from there', async ({ page }) => {
  await installMediaShim(page)
  await mockMediaServer(page)

  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'Library' })).toBeVisible()
  await page.locator('a[href="/items/1"]').click()

  await expect(page.getByRole('heading', { name: 'Big Buck Bunny' })).toBeVisible()
  await page.getByRole('button', { name: 'Play' }).click()

  await expect(page).toHaveURL(/\/watch\/1\?file_id=10&t=0$/)
  await page.locator('[data-player-controls] a').first().click()
  await expect(page).toHaveURL('/items/1')
  await expect(page.getByRole('heading', { name: 'Big Buck Bunny' })).toBeVisible()
})

async function installMediaShim(page: Page) {
  await page.addInitScript(() => {
    type MediaState = {
      src: string
      currentTime: number
      duration: number
      paused: boolean
      timer: number | null
    }

    const states = new WeakMap<HTMLMediaElement, MediaState>()

    function ensure(element: HTMLMediaElement) {
      let state = states.get(element)
      if (!state) {
        state = {
          src: '',
          currentTime: 0,
          duration: 596.4,
          paused: true,
          timer: null,
        }
        states.set(element, state)
        bindElement(element, state)
      }
      return state
    }

    function bindElement(element: HTMLMediaElement, state: MediaState) {
      Object.defineProperty(element, 'src', {
        configurable: true,
        get() {
          return state.src
        },
        set(value: string) {
          state.src = value
        },
      })

      Object.defineProperty(element, 'duration', {
        configurable: true,
        get() {
          return state.duration
        },
      })

      Object.defineProperty(element, 'currentTime', {
        configurable: true,
        get() {
          return state.currentTime
        },
        set(value: number) {
          state.currentTime = Math.max(0, Math.min(state.duration, value))
          dispatch(element, 'timeupdate')
        },
      })

      Object.defineProperty(element, 'paused', {
        configurable: true,
        get() {
          return state.paused
        },
      })
    }

    function dispatch(element: HTMLMediaElement, type: string) {
      element.dispatchEvent(new Event(type, { bubbles: true }))
    }

    function stopTimer(state: MediaState) {
      if (state.timer != null) {
        window.clearInterval(state.timer)
        state.timer = null
      }
    }

    for (const prototype of [HTMLMediaElement.prototype, HTMLVideoElement.prototype]) {
      Object.defineProperty(prototype, 'src', {
        configurable: true,
        get() {
          return ensure(this).src
        },
        set(value: string) {
          ensure(this).src = value
        },
      })

      Object.defineProperty(prototype, 'duration', {
        configurable: true,
        get() {
          return ensure(this).duration
        },
      })

      Object.defineProperty(prototype, 'currentTime', {
        configurable: true,
        get() {
          return ensure(this).currentTime
        },
        set(value: number) {
          const state = ensure(this)
          state.currentTime = Math.max(0, Math.min(state.duration, value))
          dispatch(this, 'timeupdate')
        },
      })

      Object.defineProperty(prototype, 'paused', {
        configurable: true,
        get() {
          return ensure(this).paused
        },
      })

      prototype.load = function load() {
        ensure(this)
        window.setTimeout(() => {
          dispatch(this, 'durationchange')
          dispatch(this, 'loadedmetadata')
        }, 0)
      }

      prototype.play = function play() {
        const state = ensure(this)
        state.paused = false
        dispatch(this, 'play')
        if (state.timer == null) {
          state.timer = window.setInterval(() => {
            if (state.paused) return
            state.currentTime = Math.min(state.duration, state.currentTime + 1)
            dispatch(this, 'timeupdate')
          }, 100)
        }
        return Promise.resolve()
      }

      prototype.pause = function pause() {
        const state = ensure(this)
        state.paused = true
        stopTimer(state)
        dispatch(this, 'pause')
      }
    }
  })
}

async function mockMediaServer(page: Page): Promise<MockState> {
  const state: MockState = {
    progress: null,
    playRequests: 0,
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

    if (request.method() === 'GET' && path === '/api/events') {
      await route.fulfill({ status: 204 })
      return
    }

    if (request.method() === 'GET' && path === '/api/items') {
      await json(route, {
        total: 1,
        items: [summary(state.progress)],
      })
      return
    }

    if (request.method() === 'GET' && path === '/api/search') {
      await json(route, {
        total: 1,
        items: [summary(state.progress)],
      })
      return
    }

    if (request.method() === 'GET' && path === '/api/items/1/thumb') {
      await json(route, { error: { code: 'thumbnail_not_ready', message: 'thumbnail has not been generated yet' } }, 416)
      return
    }

    if (request.method() === 'GET' && path === '/api/items/1') {
      await json(route, detail(state.progress))
      return
    }

    if (request.method() === 'POST' && path === '/api/items/1/play') {
      state.playRequests += 1
      await json(route, {
        mode: 'direct',
        reason: null,
        url: '/api/files/10/stream',
        subtitles: [],
      })
      return
    }

    if (request.method() === 'PUT' && path === '/api/items/1/progress') {
      const payload = request.postDataJSON() as { position_s: number; duration_s: number }
      state.progress = {
        position_s: payload.position_s,
        completed: payload.position_s / payload.duration_s >= 0.95,
      }
      await json(route, state.progress)
      return
    }

    if (request.method() === 'GET' && path === '/api/files/10/stream') {
      await route.fulfill({
        status: 200,
        contentType: 'video/mp4',
        body: '',
      })
      return
    }

    await json(route, { error: { code: 'not_found', message: 'no such endpoint' } }, 404)
  })

  return state
}

function summary(progress: ProgressState | null) {
  return {
    id: 1,
    type: 'video',
    title: 'Big Buck Bunny',
    year: 2008,
    duration_s: 596.4,
    created_at: '2026-07-01 00:00:00',
    thumb_url: '/api/items/1/thumb',
    available: true,
    progress: progress ?? undefined,
    collection_ids: [],
  }
}

function detail(progress: ProgressState | null) {
  return {
    ...summary(progress),
    summary: 'A short test fixture.',
    created_at: '2026-07-04 00:00:00',
    updated_at: '2026-07-04 00:00:00',
    deleted_at: null,
    files: [
      {
        id: 10,
        root_id: 1,
        rel_path: 'movies/big-buck-bunny.mp4',
        size: 123_456,
        container: 'mp4',
        duration_s: 596.4,
        width: 1280,
        height: 720,
        bitrate: 1_000_000,
        status: 'online',
        streams: [
          { stream_index: 0, kind: 'video', codec: 'h264', is_default: true },
          { stream_index: 1, kind: 'audio', codec: 'aac', is_default: true },
        ],
      },
    ],
  }
}

async function json(route: Route, body: unknown, status = 200) {
  await route.fulfill({
    status,
    contentType: 'application/json',
    body: JSON.stringify(body),
  })
}
