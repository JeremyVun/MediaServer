import { defineConfig } from 'vitest/config'

// Unit tests for pure logic only (see src/player/playerLogic.test.ts). E2E
// stays on Playwright (npm run test:e2e).
export default defineConfig({
  test: {
    include: ['src/**/*.test.ts'],
    environment: 'node',
  },
})
