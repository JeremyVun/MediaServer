import { expect, test, type Page, type Route } from '@playwright/test'

type Root = {
  id: number
  name: string
  path: string
  online: boolean
  free_bytes: number
  file_count: number
}

type MockState = {
  roots: Root[]
  rescans: number[]
  detaches: number[]
}

test('settings manages roots with folder browser validation', async ({ page }) => {
  const state = await mockRootsServer(page)

  await page.goto('/settings')
  await expect(page.getByRole('heading', { name: 'Settings' })).toBeVisible()
  await expect(page.getByRole('region', { name: 'Media A root' })).toBeVisible()

  await page.getByRole('button', { name: 'Add root' }).click()
  const dialog = page.getByRole('dialog', { name: 'Add root' })
  await expect(dialog.getByRole('button', { name: 'Media-A' })).toBeVisible()

  await dialog.getByRole('button', { name: 'Media-A' }).click()
  await dialog.getByRole('button', { name: 'Choose this folder' }).click()
  await dialog.getByRole('button', { name: 'Add root' }).click()
  await expect(dialog.getByText('This folder overlaps an attached root.')).toBeVisible()

  await dialog.getByRole('button', { name: 'Volumes' }).click()
  await dialog.getByRole('button', { name: 'Media-B' }).click()
  await dialog.getByRole('button', { name: 'Choose this folder' }).click()
  await dialog.getByRole('button', { name: 'Add root' }).click()

  const mediaB = page.getByRole('region', { name: 'Media-B root' })
  await expect(mediaB).toBeVisible()

  await mediaB.getByRole('button', { name: 'Rescan' }).click()
  await expect.poll(() => state.rescans).toEqual([2])

  await mediaB.getByRole('button', { name: 'Detach' }).click()
  await page.getByRole('dialog', { name: 'Detach root?' }).getByRole('button', { name: 'Detach' }).click()
  await expect.poll(() => state.detaches).toEqual([2])
  await expect(mediaB).toBeHidden()
})

async function mockRootsServer(page: Page): Promise<MockState> {
  const state: MockState = {
    roots: [
      {
        id: 1,
        name: 'Media A',
        path: '/Volumes/Media-A',
        online: true,
        free_bytes: 800_000_000_000,
        file_count: 12,
      },
    ],
    rescans: [],
    detaches: [],
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
        uptime_s: 12,
        db_ok: true,
        roots: state.roots.map((root) => ({
          id: root.id,
          name: root.name,
          online: root.online,
          free_bytes: root.free_bytes,
        })),
        active_sessions: 0,
        queue_depth: 0,
      })
      return
    }

    if (request.method() === 'GET' && path === '/api/roots') {
      await json(route, state.roots)
      return
    }

    if (request.method() === 'GET' && path === '/api/fs/dirs') {
      await json(route, dirs(url.searchParams.get('path') ?? '/Volumes'))
      return
    }

    if (request.method() === 'POST' && path === '/api/roots') {
      const body = JSON.parse(request.postData() ?? '{}') as { name: string; path: string }
      if (body.path === '/Volumes/Media-A' || body.path.startsWith('/Volumes/Media-A/')) {
        await json(
          route,
          { error: { code: 'duplicate_root', message: 'path is already covered by an attached root' } },
          409,
        )
        return
      }
      const root: Root = {
        id: 2,
        name: body.name,
        path: body.path,
        online: true,
        free_bytes: 500_000_000_000,
        file_count: 0,
      }
      state.roots = [...state.roots, root]
      await json(route, root, 201)
      return
    }

    const rescanMatch = path.match(/^\/api\/roots\/(\d+)\/rescan$/)
    if (request.method() === 'POST' && rescanMatch) {
      state.rescans.push(Number(rescanMatch[1]))
      await json(route, { job_id: 99, status: 'queued' }, 202)
      return
    }

    const detachMatch = path.match(/^\/api\/roots\/(\d+)$/)
    if (request.method() === 'DELETE' && detachMatch) {
      const id = Number(detachMatch[1])
      state.detaches.push(id)
      state.roots = state.roots.filter((root) => root.id !== id)
      await route.fulfill({ status: 204 })
      return
    }

    await json(route, { error: { code: 'not_found', message: 'no such endpoint' } }, 404)
  })

  return state
}

function dirs(path: string) {
  if (path === '/Volumes') {
    return {
      path,
      parent: '/',
      dirs: [
        { name: 'Media-A', path: '/Volumes/Media-A' },
        { name: 'Media-B', path: '/Volumes/Media-B' },
      ],
    }
  }
  return { path, parent: '/Volumes', dirs: [] }
}

async function json(route: Route, body: unknown, status = 200) {
  await route.fulfill({
    status,
    contentType: 'application/json',
    body: JSON.stringify(body),
  })
}
