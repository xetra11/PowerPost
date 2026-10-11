import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The Go server serves the built UI from ../internal/webui/dist (embedded in Docker builds).
// In dev, Vite proxies API, media and MCP calls to the Go server on :4000.
const backend = process.env.PP_BACKEND ?? 'http://127.0.0.1:4000'

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: '../internal/webui/dist',
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': backend,
      '/media': backend,
      '/mcp': backend,
    },
  },
})
