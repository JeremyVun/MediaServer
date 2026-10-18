import { existsSync, readdirSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'
import { chromium, defineConfig, devices, webkit } from '@playwright/test'

// The installed browser builds often lag or lead @playwright/test's pinned ones;
// fall back to the newest build in the cache so e2e runs without a reinstall.
function browserPath(expected: string, prefix: string, tail: string): string | undefined {
  if (existsSync(expected)) return undefined
  const cache = join(homedir(), 'Library/Caches/ms-playwright')
  if (!existsSync(cache)) return undefined
  const dirs = readdirSync(cache)
    .filter((d) => d.startsWith(prefix) && /^\d+$/.test(d.slice(prefix.length)))
    .sort((a, b) => Number(b.slice(prefix.length)) - Number(a.slice(prefix.length)))
  const found = dirs.map((d) => join(cache, d, tail)).find((p) => existsSync(p))
  return found
}

const chromiumPath = browserPath(
  chromium.executablePath(),
  'chromium-',
  'chrome-mac-arm64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing',
)
const webkitPath = browserPath(webkit.executablePath(), 'webkit-', 'pw_run.sh')
// Parallel worktrees each need their own dev server port.
const port = process.env.E2E_PORT ?? '4173'
const launch = (executablePath: string | undefined) => (executablePath ? { launchOptions: { executablePath } } : {})

export default defineConfig({
  testDir: './e2e',
  testIgnore: process.env.CAPTURE ? [] : ['capture/**'],
  timeout: 30_000,
  expect: {
    timeout: 5_000,
  },
  reporter: [['list']],
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    trace: 'on-first-retry',
  },
  webServer: {
    command: `npm run dev -- --host 127.0.0.1 --port ${port} --strictPort`,
    url: `http://127.0.0.1:${port}`,
    reuseExistingServer: false,
    timeout: 30_000,
  },
  projects: [
    {
      name: 'desktop-chromium',
      use: { ...devices['Desktop Chrome'], ...launch(chromiumPath) },
    },
    {
      name: 'desktop-safari',
      use: { ...devices['Desktop Safari'], ...launch(webkitPath) },
    },
    {
      name: 'ipad-safari',
      use: { ...devices['iPad Pro 11 landscape'], ...launch(webkitPath) },
    },
  ],
})
