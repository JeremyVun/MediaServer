import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { defineConfig } from 'vite'

// Dev mode: Vite serves the app and proxies /api to the Go server on :8484.
// Production: `vite build` → dist/, embedded into the Go binary (make build).
export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    proxy: {
      '/api': 'http://localhost:8484',
    },
  },
})
