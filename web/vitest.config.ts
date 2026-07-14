import { defineConfig } from 'vitest/config'

// Unit tests for pure logic only (see src/player/playerLogic.test.ts). E2E
// stays on Playwright (npm run test:e2e).
export default defineConfig({
  test: {
    include: ['src/**/*.test.ts'],
    environment: 'node',
    // css:true lets `?raw` CSS imports resolve to real file contents (default
    // vitest stubs CSS to ''); the theme contrast test reads tokens.css and
    // the theme files this way.
    css: true,
  },
})
